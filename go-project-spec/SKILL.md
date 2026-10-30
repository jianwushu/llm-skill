---
name: go-project-spec
description: Go 项目结构规范与审计。约束目录分层（handler-service-repo）、Vue3 前端组织、embed 单二进制与前后端分离两种部署形态，并规定日志自清理、sqlite/pg/oracle 数据库、北向上报（http/redis/mq/mqtt）、请求级与常驻级任务的接入位置。附可运行 MVP 模板，模板之外只给判据与伪代码。搭建、改造或审查 Go / Go+Vue3 项目时使用。
version: 1.0.0
agent_created: true
---

# Go 项目规范

管：目录怎么分、能力放哪层、前端怎么组织、用哪种部署形态、可选集成装在哪、审查报什么。
不管：模板之外的成品代码。除 `assets/go-template/`，全部内容都是判断条件与伪代码。

## 两种模式

- **搭建 / 改造**：定部署形态 → 复制模板 → 裁掉不用的可选组件 → 逐项加集成（工作流 A–D）。
- **审计**：只读。按 R1–R16 逐条扫，输出「条款 + 文件:行 + 判据 + 结论」，判据口径见 `audit.md`。不改代码、不评风格。

两种模式都不做的事：把可选能力的缺失报成缺陷。

## 先定部署形态

| | A 单二进制 | B 前后端分离 |
|---|---|---|
| 前端 | `go:embed` 打进可执行文件 | nginx / CDN 托管，Go 只提供 API |
| embed 点 | 有且只有一处 | 不建 |
| 跨域 | 同源，无需 CORS | 必须配白名单 |
| 适合 | 内网工具、单机、离线 | 多端复用 API、前端独立发版 |

前端要独立发版或有多端复用 → B，否则 A。形态只影响 `webfs/` 与 CORS，不改分层和前端结构。

## 目录

```
cmd/server/main.go          # 唯一入口，只做装配
internal/
  config/                   # 配置唯一定义与加载（YAML）
  server/                   # 生命周期、中间件、routes.go
  handler/                  # 绑定校验 → 调 service → 统一响应
  service/                  # 业务规则、事务编排
  engine/                   # [可选] 同步能力：契约 + 调度器 + 每能力一文件
  daemon/                   # [可选] 常驻链路：Daemon 契约 + 注册表
  pipeline/                 # [可选] 算子注册表与编排（Record 归此包）
  client/                   # [可选] 外部依赖唯一构造点
  repo/                     # [可选] 数据访问 + repotest/
  model/                    # 领域实体
  logger/                   # 日志唯一初始化点
  middleware/               # 日志 / Recover / CORS / 鉴权
  webfs/                    # [形态 A] 唯一 embed 点 + dist/
web/                        # Vue3，两形态结构相同
  src/modules/<domain>/     # components/ views/ stores/ api.ts
  src/{api,router,stores,views,components,composables,utils,styles}/
configs/                    # config.yaml 首启生成，生成后 gitignore
Makefile
```

只建用到的目录。没命中触发条件就不加，不预留空目录、空接口、无人引用的骨架。

| 目录 | 什么时候加 |
|---|---|
| `internal/webfs/` | 形态 A |
| `internal/repo/` | 有持久化或任务表 |
| `internal/client/` | 外部调用已收口（≥2 处，或需统一观测/测试替身）；1–2 处固定上游直接用标准库 |
| `internal/engine/` | 同步能力 ≥2 且互不耦合 |
| `internal/daemon/` `internal/pipeline/` | 任务源在 DB/队列、时长不可控、需重试或断点续跑 |

## 红线

违反即结构错误，审计必须报出。

**R1 分层方向唯一**：`handler → service → repo → *sql.DB`。禁止反向依赖与跨层调用。

**R2 internal 为界**：业务代码全在 `internal/`；`pkg/` 只放与业务无关的通用能力。

**R3 单一 embed 点**（形态 A）：全项目一处 `//go:embed` 前端产物，embed 目录与 vite `outDir` 一致，禁止多份 dist。位置不限——`internal/webfs/` 与 `web/embed.go` 等价，判据是「唯一 + 目录一致」。形态 B 不留 embed 文件与副本。

**R4 请求与响应各一个出口**：后端唯一响应助手，前端唯一请求内核。字段名与成功码语义项目自定，但两端必须一致，不得绕过出口另造格式。

**R5 驱动纯 Go**：禁 CGO/OCI 驱动，保证 `CGO_ENABLED=0` 可编译。数据库种类按需求选，不要求全实现。

**R6 方言收敛在 repo**：占位符、分页、时间函数、自增等差异只出现在 repo 内部。

**R7 配置单一来源**：唯一定义与加载点是 `internal/config`，其他包禁散读文件或环境变量。

**R8 日志唯一初始化点**：位于配置校验之后、其他组件之前。

**R9 外部依赖收口后只在 `internal/client` 构造**：装配期创建、登记逆序关闭、最小接口注入；禁止包级单例与包级可变开关。尚未收口（1–2 处固定上游）时直接用标准库不算违规。

**R10 多能力必须有注册表**：同步走 `engine`，常驻走 `daemon`。禁止在 router/handler 里按能力写 `switch`，也禁止为每个能力单独开路由。常驻链路的 `/api/{module}/status` 由注册表遍历生成，不算逐个开路由。

**R11 常驻链路四件套**：统一 `Daemon` 契约、明确状态机、抢占/租约 + 超时回收、重试上限与优雅排空。

**R12 SPA 回退**（形态 A）：未命中的非 API 路径回退 `index.html`；带扩展名未命中的静态资源返回 404；未注册的 `/api/*` 返回 JSON 404，不得落入 SPA 回退。形态 B 这归静态服务器。

**R13 前端组件就近放置**：模块级复用组件放 `modules/<domain>/components/`（扁平档 `views/<domain>/components/`），不得放全局。全局 `components/` 只容纳同时满足三条的组件：① 被 ≥2 个模块使用；② 无业务语义（不 import 业务 api / 业务 store / 业务类型 / 路由）；③ 纯 props/emit/slots 通信。模块间禁止直接 import 对方组件。应用级布局组件（`AppHeader` / `AppSidebar` / `AppLayout`）例外：可用 app 级 UI store（主题、侧栏折叠、语言、显示名），含领域实体或调业务 api 的就是业务 store，不能用——把那段拆成业务组件走 slot 挂进去。

**R14 关闭逻辑只有一份**：逐资源登记 closer，装配失败与正常退出走同一套逆序关闭；顺序 `HTTP Shutdown → daemon Stop（先 Pause 排空）→ client Close → db.Close → logger Close`。禁止"成功路径才关"的双份逻辑。

**R15 出站调用带 ctx 与显式超时**：对外方法第一个参数是 `context.Context`；禁止零值 `&http.Client{}`、禁止无超时的 Dial。重试只给幂等方法（GET/PUT/DELETE 与网络错误、5xx、429），POST/PATCH 默认不重试。

**R16 并发执行有界**：信号量或固定 worker 池；满了立即返回"忙"而不排队；单次执行带超时；单个任务的 panic 不得带走进程。

## 风格等价（差异不判错）

- **S1 命名**：`handler↔controllers`、`service↔services`、`repo↔repository`、`model↔entity`、`server+middleware↔router`、`internal/logger↔pkg/logger`、`internal/webfs/↔web/embed.go`。看职责与依赖方向，不看目录名。
- **S2 接口**：推荐 `/api/v1`；既有项目统一 `/api` 且无版本诉求时保持一致即可。
- **S3 响应**：`{code,msg,data}` 与 `{code,message,data}` 均可；`code=0` 与 `code=HTTP 状态码` 均可。前后端同步即可。
- **S4 构建**：`Makefile` 与 `.bat` 等价，Windows 现场优先便携脚本。
- **S5 配置缺失**：生成默认配置并退出，或内置默认值继续启动，均可；后者必须打印明确告警。
- **S6 前端**：TS 与 JS 等价；模块化档与扁平档均合法，但 R13 判据完全一致——不能因为"扁平档没有 modules 目录"就把模块组件塞进全局。

## 工作流

### A. 新建项目

1. 定部署形态（A/B），它决定是否建 `webfs/` 与 CORS 配置。
2. 复制 `assets/go-template/`，全局替换 module 名。
3. 先跑通依赖，否则后续构建全失败：`go mod tidy && go build ./... && go vet ./... && go test ./...`
4. 裁剪可选组件：`engine/` `client/` `daemon/` `pipeline/` `repo/` 按需求保留，其余删除并清掉 `main.go` 装配块与 config 配置节。形态 B 同时删 `internal/webfs/`，`http.embed_frontend` 置 false。
5. `npm create vue@latest web`，按 `frontend.md` 整理 `web/src`，删脚手架示例页。
6. 配 `vite.config.ts`：形态 A `outDir` 指向 embed 包内的 dist + `emptyOutDir` + `/api` 代理；形态 B 默认 `dist` + `VITE_API_BASE`。
7. 验证：形态 A `make build` 后 `/` 出页面、`/api/v1/health` 正常；形态 B 未匹配路径返回 JSON 404、跨域预检 204。

细节见 `mvp.md`。

### B. 新增业务模块

后端 `model/` → `repo/` → `service/` → `handler/` → `routes.go`；前端建 `modules/<domain>/{views,components,stores,api.ts}`，模块级组件不放全局。做完按红线自查。

### C. 新增能力

- **同步能力**：`engine/<name>.go` 实现 `Engine` → 补用例 → 装配处 `Register` 一行，路由与 handler 不动 → 前端加 view。
- **常驻链路 / 算子**：实现 `daemon.Daemon` → `registry.Register(unit)`，运维路由自动生成；算子实现 `pipeline.Step` 并 `Register`，不改编排器。

细节见 `tasks.md`。

### D. 接入集成

1. `config` 加配置节；`internal/client/<dep>.go` 实现（ctx + 显式超时 + 错误分类）；装配处创建并登记关闭。
2. 按方向归位（`integration.md`）：
   - **只会发**（HTTP / Redis / MQ 生产端）：暴露一个方法，按最小接口注入给需要它的功能。
   - **还要收**（MQTT / MQ 消费端 / WebSocket）：客户端先建，业务 handler 后注册。接收侧是轻量常驻，不新建层、不套五态状态机，判定见 `tasks.md`。

### E. 审计既有项目

按 `audit.md` 走：先定形态，再逐条判据取证，风格差异单列不阻断，迁移按「每步可编译」拆解。

## 参考

- `structure.md` — 目录职责、分层依赖、命名等价、响应与错误、测试替身、反模式。
- `mvp.md` — 模板复制与裁剪、embed 约束、形态 B 四处改动、构建、坑表。
- `config-logging.md` — YAML 单来源、首启生成、日志轮转与过期清理。
- `database.md` — 纯 Go 驱动矩阵、三方言收敛、建库点、事务、测试替身。
- `integration.md` — 北向集成落位：收发方向归位、HTTP 收口判据、目标校验、MQTT 细节。
- `tasks.md` — 请求级任务（dispatcher）与常驻级任务（pipeline/daemon）、处理类任务、判定边界。
- `shutdown.md` — 装配顺序、失败回滚、降级、排空与关闭。
- `frontend.md` — Vue3 目录两档、组件层级判据、api 层、错误契约、Pinia/Router、vite。
- `audit.md` — 审计判据与取证口径。
- `assets/go-template/` — 可运行 MVP 骨架（net/http + sqlite）。
