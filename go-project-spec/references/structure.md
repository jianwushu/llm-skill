# 项目结构与分层

## 目录职责

| 目录 | 职责 | 禁止事项 |
|---|---|---|
| `cmd/server/` | 程序入口，装配依赖并启动 | 业务逻辑、直接访问数据库 |
| `internal/config/` | 配置定义、加载、默认文件生成、校验 | 散读环境变量、绕过 config 写死配置 |
| `internal/server/` | HTTP 生命周期、路由注册、中间件挂载；只接收建好的依赖 | 业务逻辑；创建 repo/client/engine（装配在 `main`） |
| `internal/handler/` | 参数绑定/校验、调 service，组装统一响应 | 直接访问 `*sql.DB` 或 repo；写业务规则 |
| `internal/service/` | 业务规则、跨 repo 事务编排 | import handler；拼 SQL |
| `internal/repo/` | SQL 与查询实现，接口定义在此 | import service/handler |
| `internal/repo/repotest/` | repo 接口的内存实现（仅测试引用） | 被生产代码 import |
| `internal/model/` | 领域实体结构体 | 含 IO、HTTP、框架代码 |
| `internal/engine/` | 同步能力：契约、调度器、每能力一文件 | 引擎内写 HTTP 响应、按能力在 handler 写分支 |
| `internal/daemon/` | 常驻链路注册表、生命周期、运维契约 | 依赖 handler；按 module 写 switch |
| `internal/pipeline/` | 算子注册表与编排；`Record` 归此包 | 内含具体业务算子；反向依赖 daemon 注册表 |
| `internal/client/` | 外部依赖唯一构造点 | 业务层直接 new 客户端 |
| `internal/logger/` | 日志唯一初始化点 | 领域逻辑 |
| `internal/middleware/` | 通用 HTTP 中间件 | 领域逻辑 |
| `internal/webfs/` | embed 静态资源与 SPA 服务（仅形态 A） | 第二处 embed；形态 B 仍保留本目录 |

`repo/`、`client/`、`engine/`、`daemon/`、`pipeline/` 按需求引入；没有需求就不建目录、不留空接口。

## 分层与依赖

- 依赖方向唯一：`handler → service → repo`。上层只依赖下层接口或构造函数。
- 构造注入贯穿全链路：`repo.NewXxx(db) → service.NewXxx(repo) → handler.NewXxx(service)`，在装配处完成，不用全局变量传递依赖。
- `model` 例外：各层可引用，但必须保持纯数据。
- **不对外暴露 DB 语义**：表结构差异与字段映射由 repo 内部处理，对外输出 model。

## 命名

- 文件按领域命名：`user_repo.go`、`user_service.go`、`user_handler.go`、`model/user.go`。
- repo 接口定义在 repo 包（`type UserRepo interface`），service 依赖接口而非实现，便于测试替身。
- 构造函数统一 `NewXxx` 前缀。
- 常量集中：驱动名、错误码定义为包级常量，不散落魔法字符串。

命名等价见 SKILL.md 的 S1，判定原则是**看职责与依赖方向，不看目录名**。命名差异只作「一致性建议」提示，不阻断。

## 统一响应与错误

- 响应结构项目自定，但必须**前后端一致且唯一出口**。`code=0` 或 `code=HTTP 状态码` 都行，字段 `msg` 或 `message` 都行。
- handler 包提供助手（`WriteOK` / `WriteFail`），全部 handler 复用，禁止各写各的。前端解包规则以后端实际字段为准（见 `frontend.md` 错误契约）。
- 错误分层：
  - repo 返回原生错误或包装后的领域错误（`fmt.Errorf("query user: %w", err)`）。
  - service 定义业务错误哨兵（如 `ErrUserNotFound`），handler 负责映射状态码与业务 code。
  - 未知错误统一 500 + 通用文案，**不把内部细节泄漏给前端**；详情只进日志。
  - 校验类错误必须可被 `errors.Is` 判定（如 `ErrInvalidInput`），否则会被当成 500。

## 测试替身

- **repo 内存实现放 `internal/repo/repotest/`**，仅测试 import，不进生产二进制。生产代码 import `repotest` 属结构错误（拖入测试依赖）。
- 常驻链路的**时钟与取批函数必须可注入**：`now func() time.Time`、`claim func(ctx) ([]Record, error)`。否则「租约超时回收」「批次抢占」只能靠 sleep 测，既慢又不稳。
- **单轮 e2e 性价比最高**：内存 repo + fake 上游 → 跑一轮 → 断言状态流转与计数，秒级覆盖状态机、重试、跳过语义三条主线。
- 不追求覆盖率数字；优先覆盖「状态流转 + 幂等 + 租约回收 + 错误分类」。

## 部署形态对后端的影响

结构不变，只有装配与路由两处分叉。

| 项 | A. embed 单二进制 | B. 前后端分离 |
|---|---|---|
| `webfs` 装配 | `webfs.Handler()` 挂 `/` | 不装配，删除 `internal/webfs/` |
| 配置 | `http.embed_frontend: true` | `false` |
| 未匹配路径 | SPA 回退 `index.html` | 统一 JSON 404 |
| `/api/*` 未注册 | 强制 JSON 404，禁止落到 SPA 回退 | 同上 |
| 跨域 | 同源，CORS 仅开发期需要 | 生产必须配 `allowed_origins` 白名单 |

形态 B 三条约束：

1. **CORS 只由中间件从配置读取**，禁止在 handler 里逐个写 `Access-Control-*`；生产白名单禁止 `*`（带凭证时浏览器直接拒绝，且是越权风险）。
2. **前后端版本可能不同步**：新增字段要让前端可忽略；删字段或改语义先让前端停用；接口变更走 `/api/v2` 而非原地改 v1。
3. **静态文件与 API 不同源**：不能依赖 `Referer` 与同源 Cookie 默认策略；鉴权优先 `Authorization` 头。

## 反模式（审计必须拦截）

- `main.go` 里出现 SQL 或业务判断。
- handler 直接持有 `*sql.DB`。
- 在 `pkg/` 或根目录堆业务代码。
- 全局单例 `var DB *sql.DB` 代替构造注入。
- 包级 `sync.Once` 共享 client + 包级可变开关（`SetDebugFlag` 之类）：并发数据竞争、测试互相污染。
- 装配中途失败不回滚：DB 打开后 server 初始化失败直接 `os.Exit`。
- `server.New` 里创建 repo/client 并自己持有 closer：关闭逻辑变成两份，且中途失败时那份尚未登记。
- 可选依赖失败即 `os.Exit`：主功能其实不需要它。
- 前端静态资源被多处 embed 或手工复制多份。
- 用 `mattn/go-sqlite3`、`godror` 等 CGO 驱动，破坏交叉编译。
- 常驻链路 `pause` 只改标志位，已排队批次仍拉起新任务。
- 生产代码 import `internal/repo/repotest`。
- 单文件超千行的"上帝文件"：按领域拆到三层。
