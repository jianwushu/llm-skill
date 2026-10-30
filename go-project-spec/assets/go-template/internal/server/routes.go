package server

import (
	"net/http"

	"example.com/app/internal/handler"
)

// NewMux 注册全部路由：
//   - 业务 API 统一挂 /api/v1 前缀（版本升级走 v2 新前缀，不破坏 v1）
//   - 工具能力走统一任务入口 /api/v1/tasks，按引擎名分发，不按能力逐个建路由
//   - 其余路径交给前端静态资源（embed + SPA 回退）
//
// users / tasks 为 nil 表示对应能力未启用——数据库 driver=none、或项目没有同步引擎，
// 此时不注册相关路由，改由 /api/ 兜底返回 JSON 404（不要返回 panic 或 HTML）。
// frontend 为 nil 表示形态 B，未匹配路径统一 JSON 404。
func NewMux(users *handler.UserHandler, tasks *handler.TaskHandler, frontend http.Handler, version string) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		handler.WriteOK(w, map[string]string{"status": "ok", "version": version})
	})

	// 示例业务资源（可选组件：仅在启用数据库时注册）
	if users != nil {
		mux.HandleFunc("GET /api/v1/users", users.List)
		mux.HandleFunc("GET /api/v1/users/{id}", users.Get)
		mux.HandleFunc("POST /api/v1/users", users.Create)
	}

	// 工具能力：能力发现 + 统一任务提交。未启用引擎（tasks 为 nil）时整块跳过，
	// 请求落到下面的 /api/ 兜底返回 JSON 404，而不是 panic。
	if tasks != nil {
		mux.HandleFunc("GET /api/v1/engines", tasks.Engines)
		mux.HandleFunc("POST /api/v1/tasks", tasks.Submit)
	}

	// 未注册的 API 路径必须返回 JSON 404，不能落入下面的前端 SPA 回退——
	// 否则前端拿到的是 index.html（200/text-html），会被误判成成功响应。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		handler.WriteFail(w, http.StatusNotFound, 40404, "api not found")
	})

	// 前端静态资源与 SPA 回退（仅 embed 形态）；
	// 前后端分离形态 frontend 为 nil，未匹配路径统一返回 JSON 404，绝不返回 HTML。
	if frontend != nil {
		mux.Handle("/", frontend)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			handler.WriteFail(w, http.StatusNotFound, 40400, "not found")
		})
	}

	return mux
}
