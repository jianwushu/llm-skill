// Package webfs 是全项目唯一的 embed 点：内嵌前端构建产物并提供 SPA 静态资源服务。
// 前端构建产物由 vite 直接输出到本目录的 dist/（见 web/vite.config.ts 的 outDir）。
package webfs

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler 返回前端静态资源处理器：
//   - 命中静态文件 → 直接返回
//   - 未命中且路径带扩展名（.js/.css/.png…）→ 404，不回退（否则浏览器把 HTML 当 JS 解析）
//   - 未命中且路径无扩展名 → 回退 index.html（SPA 前端路由）
//   - dist 内无 index.html（前端未构建）→ 503 提示
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// 静态资源（带扩展名）缺失必须 404：若回退 index.html，
			// 浏览器会把 HTML 当脚本/样式解析，报错信息极难定位。
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(sub, "index.html"); err != nil {
				http.Error(w, "frontend not built: run `make web-build`", http.StatusServiceUnavailable)
				return
			}
			// SPA 回退
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}
