# 项目名

Go + Vue3 Web 应用骨架：**最小运行结构 + 按需集成的可选组件**，支持两种部署形态（`go:embed` 单二进制 / 前后端分离）。

## 部署形态（二选一，结构不变）

| | A. 单二进制 embed | B. 前后端分离 |
|---|---|---|
| 前端托管 | Go 进程内（`internal/webfs`） | nginx / CDN |
| `http.embed_frontend` | `true` | `false` |
| 未匹配路径 | SPA 回退 `index.html` | JSON 404 |
| 跨域 | 同源，无需配置 | 必须配 `http.allowed_origins` |

切换只改配置（+ 形态 B 删除 `internal/webfs/` 与 `main.go` 里那段 frontend 装配），不动分层与前端目录结构。

## 结构（必选 / 可选）

必选（最小闭环，缺一不可）：

- `cmd/server/` 唯一入口：只做装配（配置 → 日志 → repo → server），含**装配失败回滚**与逆序关闭
- `internal/{config,logger,server,handler,service}/`
- `web/` Vue3 前端（Vite + Pinia + Router）
- 形态 A 额外必选：`internal/webfs/`（唯一 embed 点），前端产物由 vite 直接输出到 `internal/webfs/dist`

可选组件（**需求里没有就删掉目录与装配行，不要留空骨架**）：

| 目录 | 用途 | 何时保留 |
|---|---|---|
| `internal/repo/`（含 `repotest/`） | 数据访问层、按需纯 Go 驱动、内存测试替身 | 需要持久化 |
| `internal/client/` | HTTP/MQTT/MQ/Redis 客户端唯一构造点（超时/重试/生命周期） | 需要调外部服务 |
| `internal/engine/` | **同步能力**引擎：每能力一文件，Dispatcher 调度（限流/超时/panic 隔离） | 同步能力 ≥ 2 |
| `internal/daemon/` | **常驻链路**运维契约 + 注册表（Pause/Resume/ResetStale/排空停机） | 轮询/批次抢占型任务 |
| `internal/pipeline/` | 算子（步骤）注册表 + 编排器；新增算子只加文件 + 一处 Register | 有多步骤处理链 |

删除可选组件时同步清理：`cmd/server/main.go` 里的装配块、`routes.go` 里的路由、`configs/config.yaml` 里对应配置节。

## 开发

首次使用先整理依赖（`go.mod` 不含依赖的具体版本，不跑这步直接 build/test 会报未声明依赖）：

```bash
go mod tidy && go build ./... && go vet ./... && go test ./...
```

```bash
make dev        # 后端 :8080
make web-dev    # 前端 :5173（/api 代理到 :8080）
go test ./...   # 单元测试（引擎/日志/守护注册表/流水线/服务层）
```

## 构建

```bash
make build          # 当前平台单二进制 bin/app
make build-linux    # linux/amd64 交叉编译（CGO_ENABLED=0）
```

## 运行配置

全部配置来自 `configs/config.yaml`，**默认不读环境变量**。
**首次启动**若未找到配置文件，会自动生成含全部默认项的 `configs/config.yaml` 并退出（打印提示），按需修改后重新启动即可。

```yaml
http:
  addr: :8080
  embed_frontend: true       # true=内嵌前端（单二进制）；false=前后端分离，仅提供 API
  allowed_origins:           # 跨域白名单（分离形态必配，生产禁用 "*"）
    - '*'
  allow_credentials: false   # 带 Cookie 凭证时置 true，且 allowed_origins 不能是 "*"
database:
  driver: sqlite        # none / sqlite / postgres / oracle；none=不启用数据库
  dsn: ""               # 留空按驱动取默认值
  max_open_conns: 10    # SQLite 遇 busy 可设为 1
engine:
  max_concurrent: 4     # 同时执行的任务上限，满则返回 429
  timeout_s: 30         # 单次执行超时
client:
  http_timeout_s: 10    # 外部 HTTP 单次超时
  http_retries: 2       # 幂等请求重试次数，0 关闭重试
  ssrf_policy: deny_private   # deny_private | allow_private | allow_hosts
  ssrf_allow_hosts: []  # allow_hosts 时必填：api.partner.com / *.corp.example / 10.10. / 192.168.0.0/16
log:
  dir: logs
  level: info           # debug / info / warn / error
  rotate: daily         # 默认按天轮转 app-YYYY-MM-DD.log
  retention_days: 7     # 过期日志自动删除
  clean_interval_hours: 24
```

`database.driver: none` 时程序不建数据库连接，仅提供 HTTP + embed 前端，启动时打一条降级警告。

### 出站目标策略（SSRF）

URL 来自用户输入时用 `deny_private`（默认，拒绝回环/私网）；上游是配置里的可信内网服务（`10.x`、`192.168.x`、`127.0.0.1`）时改 `allow_private`；半动态上游用 `allow_hosts` + 白名单。

云元数据地址（`169.254.169.254` 等）在**任何**策略下都会被拒绝。校验分三层：入口 URL、拨号解析出的 IP（防 DNS Rebinding）、每次重定向。

### 驱动按需引入

`internal/repo/db.go` 默认只 import `modernc.org/sqlite`。需要 PostgreSQL/Oracle 时取消对应行的注释并 `go mod tidy`；未注册的驱动启动时会给出明确提示（而不是运行时 500）。

### 前后端分离（形态 B）要点

1. `http.embed_frontend: false` —— 未匹配路径返回 JSON 404（不会返回 HTML 让前端误判）；
2. `http.allowed_origins` 填前端域名；用 Cookie 会话时再开 `allow_credentials`（配置会拒绝「通配 + 凭证」组合）；
3. 前端 `VITE_API_BASE` 指向后端地址，构建产物交 nginx/CDN；
4. SPA 回退由静态服务器负责：`try_files $uri $uri/ /index.html;`；
5. 形如 `/api/*` 的未注册路径始终返回 JSON 404，不会被前端回退掩盖。

## 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/health` | 健康检查（含版本号） |
| GET | `/api/v1/engines` | 能力发现（已注册引擎清单） |
| POST | `/api/v1/tasks` | 统一任务提交，按 `engine` 分发 |
| GET/POST | `/api/v1/users` | 示例业务资源（`driver: none` 时不注册） |

未注册的 `/api/...` 路径返回 JSON 404，不会落入前端 SPA 回退（避免前端把 HTML 当业务数据）。

任务提交示例：

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"engine":"text-stats","payload":{"text":"hello world","keyword":"hello"}}'
```

## 扩展点

- **新增同步能力**：`internal/engine/<name>.go` 实现 `Engine`，`cmd/server/main.go` 的 `dispatcher.Register(...)` 加一行（**返回 error，必须处理**），路由与 handler 不动。重名注册会报错而非静默覆盖。
- **新增常驻链路**：实现 `daemon.Daemon` → 装配处 `registry.Register(daemon.Unit{...})` → 运维接口（status/progress/pause/resume/reset-stale）由注册表自动生成。
- **新增算子**：`internal/engine/<format>_steps.go` 实现 `pipeline.Step`，一处 `Register`；**不要改 `pipeline.Registry.Run`**。并行分支前先 `Record.Clone()`。

## 组件规范出处

本模板是技能 `go-project-spec` 里**唯一**带真实实现的代码，组件怎么写由技能 reference 约束（reference 只有判据与伪代码，没有第二份可编译实现）：

| 组件 | 规范 |
|---|---|
| `internal/logger` | `references/config-logging.md`「日志」 |
| `internal/client` | `references/integration.md`「位置与注入」「HTTP」 |
| `internal/engine` | `references/tasks.md`「请求级任务」 |
| `internal/daemon` `internal/pipeline` | `references/tasks.md`「常驻链路」 |
| `internal/repo` | `references/database.md` |
| `cmd/server/main.go` 装配 | `references/shutdown.md` |
| 目录与分层 | `references/structure.md` |

改动组件后跑 `gofmt -l . && go build ./... && go vet ./... && go test ./...`。
