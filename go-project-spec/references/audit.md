# 审计判据

只读取证，不改代码。结论分三档：**阻断**（红线）、**建议**（风格与一致性）、**待确认**（证据不足）。

## 前置：先定形态

R3 / R12 只在形态 A 判定。形态 B 改判两件事：无 embed 文件残留、未匹配路径不返回 HTML。形态判不准时先看 `http.embed_frontend` 与是否存在 `//go:embed`，两者矛盾就列为待确认，不猜。

## 取证口径

- 每条结论必须带 `文件:行`，只写观察到的事实，不写"可能""也许"。
- 判据看**依赖方向与职责**，不看命名；命名差异只能进「建议」。
- 只报"该有的没有"，不报"可选能力没做"。没有 `repo/`、`client/`、`daemon/` 都不是缺陷。
- 拿不到证据（生成代码、外部依赖、编译期行为）就记「待确认」并说明需要什么才能确认。

## 红线判据

| 条款 | 算命中 | 不触发 |
|---|---|---|
| R1 分层方向 | service import handler/repo 实现；handler 出现 `*sql.DB`、SQL 或业务分支 | 类型定义在 model、接口在 repo |
| R2 internal 为界 | `pkg/` 或根目录出现业务实体、业务 service | `pkg/` 放纯工具（时间、字符串） |
| R3 单一 embed | 形态 A 下 `//go:embed` 前端产物 ≥2 处；embed 目录 ≠ vite `outDir` | 形态 B；`web/embed.go` 替代 `internal/webfs/` 且只有一处 |
| R4 唯一出口 | 后端两个响应结构或两套 `writeJSON`；前端第二个 axios 实例且解包规则不同 | 同一助手的多个方法 |
| R5 纯 Go 驱动 | `go.mod` 出现 `mattn/go-sqlite3`、`godror`；`CGO_ENABLED=0 go build` 失败 | 只用一种库、按需引入 |
| R6 方言收敛 | repo 之外出现 `?`/`$1`/`:1` 占位符、`LIMIT ? OFFSET ?`、`NOW()`、自增语法 | 单库单方言也要收敛，但只在 repo 内出现即合规 |
| R7 配置单源 | 非 `config` 包出现 `os.Getenv`、读取配置文件、硬编码连接串 | 测试内的临时配置 |
| R8 日志单点 | 存在两处 logger 构造；初始化早于配置校验 | 包级 `slog` 调用（用默认 logger 打点） |
| R9 依赖收口 | 业务层 `new` 客户端；包级 `var client` + 包级 setter 开关 | 1–2 处固定上游直接用标准库；`client` 包内部持有连接 |
| R10 注册表 | router/handler 按能力名写 `switch`；每个能力单独开路由 | 从注册表遍历生成的状态路由 |
| R11 常驻四件套 | 有常驻任务但缺状态机、缺租约、缺重试上限、缺排空，任一缺失 | 项目没有常驻任务 |
| R12 SPA 回退 | 形态 A 下未注册 `/api/*` 返回 HTML 或 200；带扩展名的缺失资源回退 `index.html` | 形态 B（改判 nginx 回退与 JSON 404） |
| R13 组件归属 | 模块组件放 `src/components/` 且仅 1 个模块使用；全局组件 import 业务 api/store/实体类型；模块间直接 import 对方组件 | 布局组件用 app 级 UI store（外壳字段）；被 ≥2 模块使用的纯展示组件 |
| R14 关闭一份 | 成功路径 return 前不关资源；两套关闭逻辑；closer 未逆序；logger 早于其他资源关闭；资源在 `server.New` 内创建且 closer 挂在 `Server` 字段上 | 单资源程序 |
| R15 ctx 与超时 | 出站方法无 `context.Context`；零值 `&http.Client{}`；无超时 Dial；POST 配重试 | 进程内纯计算；不重试时不要求错误细分 |
| R16 并发有界 | 每请求起无界 goroutine；队列满阻塞等待；无单次执行超时；任务 panic 未 recover | 单线程 CLI；串行调用 |

## 常见误报（不要报）

- **命名差异**：`controllers/`、`repository/`、`entity/`、`router/` 都等价。`/api` 与 `/api/v1` 都行。
- **形态与路径**：`web/embed.go` 嵌 `web/dist` 完全合规；`internal/webfs/` 不是必须的。
- **前端档位**：扁平档（`views/<domain>/components/`）合法，不能因为"没有 modules 目录"判 R13。
- **没做防护**：地址全写在配置里的内网调用不做目标校验、不做响应体限流，是正确取舍。
- **可选能力缺失**：没有客户端、没有 repo、没有常驻链路，都不是错误。
- **文件长度**：300–800 行的 handler/service 文件正常，"上帝文件"指单文件承担多个领域职责。
- **依赖降级**：`driver: none` 下 repo 返回 `nil` 且上层判空，是设计而非缺陷。

## 输出格式

```text
阻断（n）
- R6 internal/service/user_service.go:88 — service 内出现 "LIMIT ? OFFSET ?"
- R12 internal/server/routes.go:41 — 未注册 /api/* 落入 SPA 回退，返回 200 HTML

建议（n，不阻断）
- S1 controllers/ 与 service/ 单复数混用
- 响应结构两种并存：handler/user.go:12、handler/task.go:9

待确认（n）
- R3 前端 dist 构建路径无法从仓库判断，需看 vite.config.ts 与构建脚本
```

规则：阻断项必须给最小修复动作（改哪一行、加哪个函数）；建议项只说差异，不给改造方案；不复述代码。

## 迁移顺序

只在用户要求整改时执行，且每步可编译：

1. 先补测试替身与装配回滚——否则移动代码时无法验证行为不变。
2. 再移动文件（按领域拆到三层），每步过 `go build ./... && go vet ./...`。
3. 最后清死代码与空目录，不要和移动混在同一步。
