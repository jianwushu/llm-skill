# 任务与流水线

程序里的"处理"分三种形态，先判定再建结构。判错方向的代价最大：给轻量任务套状态机是白付复杂度，给重任务用同步接口是必然超时。

## 形态判定

| 形态 | 触发 | 结构 | 入口 |
|---|---|---|---|
| **请求级**（同步引擎） | 调用方等结果，秒级内完成 | `Engine` 契约 + `Dispatcher` | `POST /api/v1/tasks`，能力由 `engine` 字段区分 |
| **常驻链路**（daemon） | 任务源在 DB/队列，时长不可控，失败要可追溯 | `Daemon` + `Registry` + 五态状态机 + 租约 | 定时扫描 + 运维接口 |
| **轻量常驻** | 消息到达即处理、失败重下轮再来 | 回调 + 带缓冲队列 + 固定 worker | 无独立入口 |

**倾向常驻链路的信号**（组合判断，不是命中一条就上全套）：任务源在 DB/队列需要查询筛选、执行时长不可控、失败必须可重试或断点续跑、多实例不能重复处理、批次万级以上。

**不要过度设计**：

- 每分钟一次的健康检查（单实例、失败下轮再查）→ 一个 `time.Ticker` + 函数，不要五态状态机、租约、运维接口。
- 单实例、任务量小的目录同步 → `service/` 里一个循环 + 重试计数就够，不必拆 `daemon/` + `pipeline/`。
- 只在启动时跑一次的初始化 → 直接在装配里做，失败返回错误。
- MQTT 订阅消费 → 轻量档。注册表 + 固定 worker，只有出现"多实例竞争同一份待办"或"需要断点续跑"才升级。

**升级路径**：先写轻量版。出现「多实例竞争」「断点续跑」「按白名单动态扫」「给运维看进度」中的两个以上时再升级，状态机与租约是必补的两项。

三种形态**并列而非互斥**：一个程序可以既有同步引擎（即时试跑）又有常驻链路（批量处理落地文件），复用同一套算子实现。

## 请求级任务

### 何时拆引擎

| 信号 | 权重 |
|---|---|
| 能力 ≥ 2 且彼此不共享状态 | 强 |
| 不同能力资源特征不同（CPU 密集 / IO 密集 / 起子进程） | 强 |
| 后续会持续新增能力（插件式扩展） | 强 |
| 只是"代码有点长""想显得整齐" | 弱，**不要为此拆** |

只有一种能力、或能力之间共享大量状态时，一个函数 + 一处调用即可，套注册表只多一层间接。

### 结构

```
internal/engine/
├── engine.go        # Engine 契约 + Dispatcher（注册表/限流/超时/panic 隔离）+ 错误哨兵
├── text.go          # 每能力一个文件，文件名即能力语义
├── image.go
└── engine_test.go
```

能力增多时可拆子包（`engine/text/`、`engine/image/`），各子包实现 `engine.Engine` 并在装配处注册；**子包不得反向依赖 engine 之外的业务层**。

### 契约

`Engine{ Name() string; Description() string; Execute(ctx, payload []byte) (any, error) }`

- 入参统一 JSON `payload`，各引擎内自定义结构体并校验；出参可 JSON 序列化，字段 `snake_case`。
- **引擎不写 HTTP**：不返回状态码、不写响应、不自己打请求日志。错误用哨兵交给上层映射。
- 错误分类（不分类上层就无法正确映射）：

| 错误 | 含义 | 映射 |
|---|---|---|
| `ErrInvalidInput` | 入参缺失/格式错误/被安全策略拒绝 | 400 |
| `ErrNotFound` | 引擎名未注册 | 404 |
| `ErrBusy` | 并发已满 | 429 |
| `DeadlineExceeded` | 执行超时 | 504 |
| 其他 | 内部故障（详情进日志，不回传） | 500 |

校验类错误用 `fmt.Errorf("%w: payload.text is required", ErrInvalidInput)` 包装：既能 `errors.Is` 判定，又保留可读信息。

### 调度器职责（不要散到 handler）

1. **注册表查找**。**同名注册必须报错**：生产装配里两个能力撞名（复制粘贴漏改 `Name()`）会静默顶掉前者，启动日志却完全正常。`Register` 校验 nil、空名、批内重名、与已注册重名，整批全有或全无；`Replace` 是唯一允许覆盖的入口，只给测试替身与显式热替换。`Register` 返回 error 必须处理，否则撞名只在运行期表现为"某个能力行为不对"。
2. **并发限流**：带缓冲 channel 作信号量，满时**立即返回 `ErrBusy` 而非排队**（排队会让请求堆在内存里，最终 OOM 或全量超时）。上限按最重引擎估算，宁小勿大，图片/视频取 `min(GOMAXPROCS, 4)` 量级。
3. **单次超时**：`context.WithTimeout`，并**要求引擎在耗时点检查 `ctx.Err()`**；不检查的实现里超时只对调用方生效，后台还在算。
4. **panic 隔离**：单个引擎崩溃转成 error，进程不崩。
5. **耗时统计**：结果带 `elapsed_ms`。

```go
select {
case d.sem <- struct{}{}:
    defer func() { <-d.sem }()
case <-ctx.Done():
    return ctx.Err()
default:
    return ErrBusy          // 满了立刻拒绝
}
```

### 能力发现

后端暴露 `GET /api/v1/engines` 返回 `[{name, description}]`，前端据此渲染工具列表，新增能力不改前端路由表。前端每个能力一个 view，由统一的 `run(engine, payload)` 提交。

### 超过秒级就换形态

不要在这一层加 jobs 队列、worker 池、状态落库——那些属于常驻链路。同步接口跑分钟级任务的结果是前端超时、后端堆积。

## 常驻链路

### 目录两档

- **扁平档**（能力 ≤ 2）：`service/<x>_daemon.go` 实现 `Daemon`，`engine/<x>_steps.go` 放算子，装配处理注册与启动。
- **拆分档**（能力 ≥ 3 或持续新增）：`internal/daemon/`（契约 + 注册表）、`internal/pipeline/`（Step 注册表）、`internal/engine/<format>_steps.go`（算子实现）、`server/daemon_routes.go`（只遍历注册表生成路由）。

拆分档三条：`daemon` 只依赖 `pipeline`/`repo` 接口，**不得依赖 handler**；**`Record` 归 `pipeline` 所有**，`daemon` 直接用，不在 daemon 里另立一套记录定义（两套字段必然漂移）；`pipeline` 不依赖 `daemon` 注册表，依赖方向单向。

### 链路契约

`Daemon` 八个方法：`Module()` 链路标识、`GetState()` 三态（running/paused/stopped）、`GetStatus()` 在途数/活跃 worker/队列深度/轮询间隔/批量/最近轮询时间、`GetProgress()` 五态计数、`Pause()` **只停拉新**、`Resume()`、`ResetStale(ctx, timeoutMinutes)` 回收租约超时记录、`InFlight()` 供停机与在线更新判断。

注册表：`Register(unit)` 中 `Module` 重复报错，`StartAll` / `StopAll`（逆序、聚合错误），路由层只遍历注册表生成 `/api/v1/daemons`、`/api/v1/{module}/status|progress|pause|resume|reset-stale`。**禁止** `switch module`，禁止把 `GetStatus` 的拼装逻辑复制到每个 handler。新增链路自动获得全套运维接口。

`pause/resume` 是**进程内状态**，多实例部署需逐实例操作——接口文档与前端提示都要写明，否则运维会以为"按一次就全停了"。状态/进度接口必须便宜：只读内存计数或单条聚合，禁止每次请求扫全表。

### 算子注册表

没有注册表时，新增一个算子要同步改四处（校验 switch、执行 switch、格式分发、前端表单），漏一处就出错。

- 契约：`Step{ Type() string; Validate(cfg json.RawMessage) error; Apply(ctx, in *Record, cfg json.RawMessage) (*Record, error) }`；`Record{ Path, Body, Meta, Fields }`；`Registry` 提供 `Register`（Type 重复报错）、`ValidateAll`（逐步骤校验 + 检查链内前置产出是否齐）、`Run`。
- **新增算子只允许「加一个文件 + 一处 Register」，不得修改 `Registry.Run`**。
- 算子不写库、不发 HTTP、不读全局配置、不改链路状态；需要这些能力的放链路层。算子配置一律通过 `cfg` 传入并在 `Validate` 中校验。
- **可变性契约**：编排器保证同一时刻只有一步持有当前记录；算子可原地改也可返回新记录。但**不得**把 `*Record` 或其字段存到包级变量、缓存或异步 goroutine——「不保留引用」才是这里"纯函数"的实际含义。并行分支或需保留输入原样时先 `Clone()`。`Fields` 里的嵌套 map/slice 约定不可变，要改就整体替换。

可选增强：`GET /api/v1/steps` 返回算子元数据，前端表单由元数据驱动渲染，新增算子零前端改动。

### 五态状态机

`pending(0)` 初始、重试未超限回退、租约超时回收、停机排空回退；`processing(1)` 批次抢占成功并写入租约；`completed(2)`；`skipped(3)` 按规则不该处理；`failed(4)` 重试达上限。

- **`skipped` 与 `failed` 不可混用**。用 `skipped` 掩盖异常会让失败率永远为 0，运维看不到问题。
- **状态回退必须条件更新**：`UPDATE ... SET status=0, lease_token=NULL WHERE id=? AND status=1 AND lease_token=?`，防止并发实例互相覆盖。
- 数值取值一旦落库即为契约，前后端、报表、SQL 都依赖它，**不得中途重排**。

### 抢占与租约

| 场景 | 做法 |
|---|---|
| PostgreSQL / Oracle | `SELECT ... FOR UPDATE SKIP LOCKED` + 同事务内 `UPDATE status=1, lease_token=?` |
| SQLite（无 SKIP LOCKED） | 条件 `UPDATE ... WHERE id IN (...) AND status=0` 后查 `RowsAffected`；配合 `journal_mode=WAL` 与写连接串行 |

三项配置缺一不可：`batch_size`（建议 ≤ 100）、`workers`（按最重任务估）、`poll_interval_s`（避免空转打满 DB）。

**扫描范围下推**：只把「已配置且启用」的白名单下推到 SQL（精确值 `IN (?)`、前缀 `LIKE ?`）；**白名单为空时整轮不查库**并把这个状态暴露给运维接口，否则上层会误以为"没有待办"；正则/多级通配/跨字段条件无法下推时必须显式告警，不允许静默退化成全表扫描。索引要与查询谓词一致，典型形态是「稀疏函数索引 + 白名单前导列」：

```sql
CREATE INDEX idx_files_pending ON files (dir_id, status) WHERE status = 0;
```

上线后关注统计信息刷新（PG autovacuum/analyze、Oracle 统计收集），否则优化器可能不走该索引。

### 失败与重试

- `max_retries` 配置化（默认 3），失败回退 `pending` 并递增 `retry_count`；退避指数 + 抖动，避免同批同时重试打爆上游。
- **错误摘要入库**（截断 1000 字符），否则排障只能翻日志。
- 超上限转 `failed` 且不再自动重试，只允许运维接口人工重置。
- 区分可重试（网络超时、上游 5xx）与不可重试（文件损坏、格式不支持）：后者直接置 `failed`/`skipped`，不要白重试三次。

### 停机排空

`收到信号 → Pause()（只停拉新）→ 等 InFlight()==0（带超时，如 30s）→ 把仍 processing 的记录按 lease_token 条件回退 pending → 关 client/DB`。

在线更新钩子看 `InFlight()`：大于 0 时返回 `409`，避免截断在途任务。`Stop` 必须幂等。

### 可观测

进度是**内存计数**，进程重启归零，前端与文档必须标「本次运行」，需要历史另做统计表。日志固定字段 `module`、`batch`、记录标识、`elapsed_ms`、`retries`；单轮结束打一条汇总（取批/成功/跳过/失败/耗时）。

## 处理类任务（文件与外部工具）

文本解析、图片压缩、格式转换都属于这一类，形态由上面的判定决定，I/O 约束固定：

- **限制大小**：读取前 `Stat` 或对 Reader 加 `io.LimitReader`。无上限的 `os.ReadFile` 遇到 4GB 文件直接把进程打死。
- **不信任扩展名**：用内容嗅探判断类型（`http.DetectContentType`、`image.DecodeConfig`），扩展名只作提示。
- **路径来自外部时校验**：`filepath.Clean` + 确认仍在允许根目录内（`filepath.Rel` 不含 `..`）。直接拼路径 = 目录穿越。
- **大对象不常驻内存**：超过几 MB 就落临时文件走流式，不要在多处同时持有多份 base64。
- **临时目录**：`os.MkdirTemp` 生成，权限 `0600`，创建成功后立刻 `defer os.RemoveAll`（panic 也要清）；输出写在同一个临时目录里随目录清理。不要手拼固定临时路径，并发会互相覆盖。
- **外部命令**：必须 `exec.CommandContext`（ctx 取消能 kill 子进程）；**禁止 `sh -c` 拼接**，用户输入只能作为独立参数或写进临时文件；stderr 截断（约 512 字符）后入日志；启动时 `exec.LookPath` 探测依赖，缺失就报"未安装 ffmpeg"而不是等请求期 500；输出大小与超时都要有界；解析前校验输出文件头，失败时给"工具版本不兼容"这类可行动信息。
- **并发**：外部工具是重资源，上限按 `min(GOMAXPROCS, 2~4)` 估，不与纯 CPU 任务共用同一个并发池；同一输出文件名不能并发写。

## 反模式

| 反模式 | 后果 | 正确做法 |
|---|---|---|
| handler 里 `switch name` 分发 | 每加一个能力改多处 | 注册表 |
| 每个能力一个路由 | 路由表与前端都要跟着改 | 统一任务入口，`engine` 字段区分 |
| 同名静默覆盖 | 撞名排查极难 | `Register` 报错 + `Replace` 显式覆盖 |
| 限流满了排队 | 内存堆积 → OOM / 全量超时 | 立即返回忙 |
| 引擎内 `go func()` 不等结果 | 请求返回后仍在跑、数据竞争 | 全部等待，遵守 ctx |
| 无 panic 隔离 | 一个能力崩溃带走进程 | `recover` 转 error |
| 引擎自己写 HTTP 响应 | 无法复用于 CLI/批处理 | 只返回结果与错误 |
| 同步接口跑分钟级任务 | 前端超时、后端堆积 | 改走常驻链路 |
| 给轻量任务套五态状态机 | 复杂度翻倍、收益为零 | 轻量档 |
| 用 `skipped` 掩盖失败 | 失败率永远为 0 | 语义分开 |
| 无租约回收 | 实例崩溃后任务永久卡死 | `ResetStale` + 条件更新 |
| `pause` 只置标志位 | 已排队批次仍拉起新任务 | 停止拉新 + 等在途 |
| 扫描 `SELECT *` 再内存过滤 | 全表捞进内存 | 谓词下推 |
| 进度接口 `COUNT(*)` 全表 | 页面轮询打满 DB | 内存计数 |
| 算子直接写库/发 HTTP | 状态与副作用纠缠、无法单测 | 副作用留在链路层 |
| 状态值中途重排 | 破坏已有数据语义 | 落库即契约 |

## 检查清单

**新增同步能力**：实现 `Engine`（Name 唯一、Description 清晰）→ 入参结构体 + 校验，错误 `ErrInvalidInput` 包装 → 耗时点查 `ctx.Err()` → 补用例（正常 + 非法入参）→ 装配处 `Register` 加一行 → 前端加 view（如需）→ README 能力清单。

**新增常驻链路**：实现 `Daemon` 全部方法 → 五态齐全、`skipped`/`failed` 分开、回退用条件更新 → 抢占实现 + 租约字段 + 超时回收 → 扫描下推、白名单空则早退 → 四项配置带默认值 → 停机排空 → `registry.Register` → README 链路清单。

**新增算子**：新建 `<format>_steps.go` 实现 `Type/Validate/Apply` → `Validate` 给可读错误（含字段名）→ `Apply` 不落库不发网络不读全局配置 → 一处 `Register`，不改编排器 → 补单测（正常 + 非法配置 + 空输入）。
