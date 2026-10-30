// Package middleware 存放通用 HTTP 中间件：日志、Recover、CORS、鉴权。
// 禁止领域逻辑进入本包。
package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Logger 请求日志：方法、路径、状态码、耗时（结构化 slog）。
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).String(),
		)
	})
}

// Recover 捕获 panic，返回 500，不泄漏堆栈给客户端。
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "err", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS 跨域中间件，白名单来自配置（http.allowed_origins）。
//
// 两种部署形态共用：
//   - embed 单二进制：同源部署，实际不会触发跨域，配置保留 ["*"] 仅便于开发期直连；
//   - 前后端分离：必须填入前端域名，生产禁用 "*"。
//
// 带凭证（Cookie）时不能回 "*"：浏览器会拒绝，因此命中白名单时回显具体 Origin
// 并附 Allow-Credentials；allowCredentials 由配置控制，默认关闭。
func CORS(allowedOrigins []string, allowCredentials bool) func(http.Handler) http.Handler {
	originSet := make(map[string]struct{}, len(allowedOrigins))
	allowAll := false
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			continue
		}
		if o != "" {
			originSet[o] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 响应随 Origin 变化，必须声明 Vary，否则中间缓存会串味
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			switch {
			case origin == "":
				// 非同源请求（curl/服务端调用），无需 CORS 头
			case allowAll:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			default:
				if _, ok := originSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					if allowCredentials {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
				}
				// 未命中白名单：不加任何 Allow-Origin，由浏览器拦截（服务端不报错）
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
			w.Header().Set("Access-Control-Max-Age", "600")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
