# 模板使用与构建

`assets/go-template/` 是唯一带真实实现的部分：net/http 路由、YAML 配置、结构化日志、sqlite repo、handler/service 分层、middleware、webfs。复制后先跑通再改。

## 复制后必做

```bash
# 1. 全局替换 module 名（go.mod、所有 import）
# 2. 跑通依赖，四步全绿再动业务
go mod tidy && go build ./... && go vet ./... && go test ./...
```

模板 `go.mod` 只声明 module 与 Go 版本，不含依赖具体版本。没跑 `go mod tidy` 就 `go test` 会报「未声明的依赖」，后面的构建问题会和依赖问题混在一起。

## 裁剪

按需求保留，其余删目录 + 删 `cmd/server/main.go` 里对应的装配块 + 删 config 里对应配置节。三处漏一处就编译不过或留下死配置。

| 目录 | 留的条件 |
|---|---|
| `internal/repo/` + `sqlite` 驱动 | 有持久化或任务表 |
| `internal/client/` | 外部调用已收口 |
| `internal/engine/` | 同步能力 ≥2 且互不耦合 |
| `internal/daemon/` `internal/pipeline/` | 常驻任务，判定见 `tasks.md` |
| `internal/webfs/` | 形态 A；形态 B 必删 |

## 形态 A：embed 约束

- `//go:embed` 的模式**不能包含 `..`**，只能嵌入包目录内的文件。所以前端产物必须由 vite `outDir` 直接输出到 embed 文件所在包的目录内，禁止"构建到 A 再复制到 B"。`web/public/.embedkeep` 用来保证 `emptyOutDir` 清空后 dist 仍非空——模式匹配不到任何文件时**编译失败**。
- 统一写 `//go:embed all:dist`：不带 `all:` 时以 `.`/`_` 开头的文件会被忽略。
- embed 文件放哪都可以，只要 dist 在它的包目录里。`internal/webfs/webfs.go` 嵌 `internal/webfs/dist/` 与 `web/embed.go` 嵌 `web/dist/`（vite 默认 outDir，不用改配置）**等价**，判据是「全项目唯一 + embed 目录 = outDir」。放 `web/` 下时包名取 `web` 或 `webembed`，import 路径变成 `<module>/web`，逻辑一字不改；**不要因为文件在 `web/` 下就再补一份 `internal/webfs/`**。
- 产物目录整体进 `.gitignore`，构建产物不入库。
- `webfs.Handler()` 的三条行为：静态文件命中直接返回；未命中且无扩展名 → 回退 `index.html`（SPA 路由）；未命中且带扩展名 → 404；dist 内没有 `index.html` → 503 提示前端未构建。Gin 项目在 `routes.go` 用 `NoRoute` 挂同一个 handler。
- 缓存：`index.html` 不缓存（`Cache-Control: no-cache`），带 hash 的资源长缓存，在中间件里按路径区分。

## 形态 B：四处改动

分层与前端模块结构**不动**，只改这四处：

1. **后端关 embed**：`http.embed_frontend: false`，删 `internal/webfs/` 与 `dist/`。未匹配路径统一返回 JSON 404，不返回 HTML。
2. **前端基址**：`VITE_API_BASE` 指向 API 前缀。构建产物输出到默认 `dist` 交给静态服务器。
3. **静态服务器 SPA 回退**：nginx `location / { try_files $uri $uri/ /index.html; }`；`/assets/` 长缓存、`= /index.html` 加 `no-cache`。
4. **CORS 白名单**：`http.allowed_origins` 列具体域名。带 Cookie 时前端 `credentials: 'include'`、后端回显具体 Origin + `Allow-Credentials: true`；用 `Authorization` 头时 `Allow-Headers` 必须含它并正确响应 `OPTIONS` 预检（204）。

最省事的做法：前后端同域不同路径（`/` 前端、`/api` 后端），由同一层 nginx 反代，**完全避免跨域**。

## 构建

| 目标 | 作用 |
|---|---|
| `dev` | 后端开发运行 |
| `web-dev` | 前端 vite 开发服务器 |
| `web-build` | 前端构建（形态 A 输出到 embed 目录；形态 B 输出到 `web/dist`） |
| `build` | web-build + `CGO_ENABLED=0 go build -ldflags "-s -w"` → `bin/` |
| `build-linux` | 交叉编译 `GOOS=linux GOARCH=amd64` |
| `tidy` / `clean` | 依赖整理 / 清理产物 |

- 一律 `CGO_ENABLED=0`。构建前确认没混入 CGO 依赖，否则交叉编译直接失败。
- 版本注入：main 包 `var version = "dev"`，`-ldflags "-X main.version=$(TAG)"`，`/api/v1/health` 返回版本号。
- Windows 无 make 时用等价命令直跑，Makefile 面向 Linux/macOS/CI。
- 双模式开发：后端 `:8080`，前端 vite `:5173` 把 `/api` 代理过去，联调只访问 `:5173`，验收用 `make build` 的单二进制。

## 坑

| 现象 | 原因 | 处置 |
|---|---|---|
| `pattern dist: no matching files found` | dist 为空 | 确认 `.embedkeep` 存在且未被清掉 |
| 新前端构建后线上仍是旧页面 | dist 双份不同步 / 浏览器缓存 | 只留一处 dist；`index.html` 加 no-cache |
| 交叉编译报 cgo 错误 | 混入 CGO 驱动 | 换纯 Go 驱动，见 `database.md` |
| 刷新非根路径 404 | 缺 SPA 回退 | 形态 A 查 webfs；形态 B 查 nginx `try_files` |
| 未注册 API 返回 index.html | 兜底路由吞掉 `/api/*` | 在 SPA 兜底之前注册 `/api/` 的 JSON 404；`/api/` 比 `/` 更具体，Go 1.22 mux 优先匹配 |
| SQLite `unable to open database file (14)` | DSN 父目录不存在 | 模板 `repo.Open` 已自动建父目录；自建连接要 `MkdirAll` |
| dev 模式前端 503 | 前端未构建 | 正常现象，开发走 vite `:5173` |
| embed 后改前端不生效 | embed 内容编译期固化 | 重新 `go build` |
| 形态 B：dev 正常、生产 CORS 失败 | 开发期走 vite 代理，从不触发跨域 | 部署前用真实域名实测一次 `OPTIONS` |
