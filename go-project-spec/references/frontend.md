# Vue3 前端结构

脚手架 `npm create vue@latest web`，按需勾选 TS / Pinia / Router，生成后按本文整理。组件多单词 PascalCase（`UserCard.vue`），composable 用 `useXxx.ts`，store 按领域命名。

## 目录两档

**模块化档**（业务域 ≥ 3 或中大型项目）

| 目录 | 职责 | 禁止 |
|---|---|---|
| `modules/<domain>/components/` | 模块级复用组件 | 被其他模块直接 import；放通用 UI |
| `modules/<domain>/views/` | 模块页面（路由对应） | 模块内 views 互相 import；import 其他模块 views |
| `modules/<domain>/stores/` | 模块私有状态 | 被其他模块引用内部 state |
| `modules/<domain>/api.ts` | 该模块接口函数 | 自建请求实例（必须用 `api/http.ts`） |
| `components/` | 全局组件：跨模块、真正通用 | 含业务语义 |
| `api/` | 请求内核 `http.ts` + 接口模块 | UI 逻辑、弹窗提示散落 |
| `router/` | 路由表与守卫 | 组件内手写 `location` 跳转 |
| `stores/` | 跨模块共享状态 | 组件私有状态、单模块状态 |
| `views/` | 扁平档页面级组件 | views 互相 import |
| `composables/` | 组合式逻辑复用 | 依赖组件实例 |
| `utils/` | 纯函数 | 引入响应式或组件 |
| `layouts/` | 应用外壳 | 依赖业务 store / 业务 api |
| `assets/` `styles/` | 静态资源与全局样式 | — |

**扁平档**（小项目）：`views/<domain>/` + `components/` + `stores/`。档位不影响组件判据。

## 组件归属

判据一句话：**组件跟着"谁用它"走，不跟着"它看起来多通用"走。**

| 层级 | 位置 | 准入 |
|---|---|---|
| 页面内组件 | 与页面同目录 | 仅当前页面使用 |
| 模块级 | `modules/<domain>/components/` | 本模块内多处复用，可含业务语义 |
| 全局 | `src/components/` | 三条同时满足：被 ≥2 模块使用、无业务语义、纯 props/emit/slots |
| 应用级布局 | `src/layouts/` 或 `components/App*` | 承载外壳；可用 app 级 UI store，不得碰业务 store/api/实体类型 |

「写了两次」不算通用——两次都在同一业务域里，它就是模块组件。

布局组件例外看 store 装什么：`theme` / `sidebarCollapsed` / `locale` / `userDisplayName` / `breadcrumbs` 属于外壳字段，可用；含 `order` / `user` / `device` 等领域实体或调业务 api，就是业务 store，不能用。一旦布局组件开始读业务数据（在 `AppHeader` 里取待办数做角标），它就不是布局组件了——把那段拆成业务组件通过 slot 挂进去。

**提升路径（模块 → 全局）**：把 api 调用、store 依赖、业务类型拔出改为 props/emit → 放 `components/` 并用 `defineProps<T>()` 类型化 → 原模块改为传参。例：`OrderTable.vue` 去掉 `useOrderStore`、改成接收 `rows` + 抛 `select`，提升为 `DataTable.vue`。

**两条替代路径**：只要逻辑复用 → 抽 `composables/`；只要样式/布局复用 → 抽 `styles/` 或布局组件。为共用三个函数而封装全局组件是错的方向。

## api 层

```
src/api/
├── http.ts          # 唯一封装点：实例 + 拦截器 + 错误契约
└── modules/user.ts  # 按后端领域拆
```

`http.ts` 必须做四件事：统一实例（`baseURL` 读 `VITE_API_BASE`，默认 `/api/v1`）、按后端实际字段解包（调用方直接拿业务数据）、错误统一处理并 reject、请求/响应类型与模块放在一起。

模块写法：`export const userApi = { list: (q) => http.get<Page<User>>('/users', { params: q }) }`，函数显式标返回类型。

## 错误契约

错误形态散落会导致"有的地方弹 HTML、有的地方静默失败"。`http.ts` 统一五条：

1. **统一错误类型** `ApiError`（带 `status` / `code` / `data` / `body` / `url`），调用方只判 `instanceof`，不解析字符串。
2. **抛错三档**：
   - `!res.ok` 或响应体 `code >= 400` → 抛 `ApiError`；
   - **2xx 但响应体不是 JSON → 抛 `ApiError`**；这一档最容易漏，SPA 回退页、反代错误页、网关登录页都会返回 `200 + text/html`，不校验 `Content-Type` 就把整段 HTML 当数据渲染；
   - 空响应（204 / `content-length: 0`）→ 返回 `null`，不抛错。
3. **非 JSON 响应按状态码映射中文兜底**（400 参数有误 / 401 登录过期 / 403 无权限 / 404 不存在 / 413 过大 / 429 频繁 / 500 / 502 网关 / 503 / 504 超时），不要把网关 HTML 吐给用户。
4. **取消语义**：`AbortError` 原样透传，不包装、不弹提示。否则组件卸载或快速切换时的正常取消会变成一片报错弹窗。
5. **查询串工具** `toQuery`：跳过 `undefined`/`null`/`''`，但**保留 `0` 与 `false`**（页码 0、开关关闭都是有效值，`if (!v)` 会把它们吞掉）。

**裸 `fetch`/`axios` 白名单**只有两处：`api/http.ts` 内部、探活/断网检测（需要非标准响应且不抛错）。其他任何组件、store、composable 里出现均属违反 R4。

## 状态与路由

- Pinia 按领域一文件，只放跨组件共享的状态与动作；组件私有状态留组件。异步取数放 store action 或 composable，组件保持薄。
- 路由集中 `router/index.ts`（或按模块 `routes.ts` 聚合）；页面懒加载；需要登录的路由用 `meta` + 全局守卫。
- 别名 `@` → `src`，脚手架默认，勿改名。

## 组件规范

- 页面组件负责组装：调 api/store、组织本模块组件，禁止裸写请求。
- 全局组件不得出现业务文案、业务 api、业务 store；props 用 `defineProps<T>()`，事件用 `defineEmits` 声明。
- 超过 3 处复用的**逻辑**进 `composables/`，不要用组件承载逻辑复用。
- 样式：全局在 `styles/`，组件内 `<style scoped>`，UI 库按需自动导入。
- **工具型应用**：每个能力一个 view，统一走 `api/modules/task.ts` 的 `run(engine, payload)`，能力列表从能力发现接口拉取渲染，新增能力不改前端路由表。
- **常驻链路型应用**：页面只调「提交 + 运维接口」（`status`/`progress`/`pause`/`resume`），轮询间隔可配（3–5s），标注「计数为本次运行」；抽 `useDaemonStatus(module)`，不要每条链路各写一套轮询。

## 构建与基址

| 部署形态 | `VITE_API_BASE` | `vite.config.ts` |
|---|---|---|
| A. embed | `/api/v1`（同源相对路径） | `outDir` 指向 embed 包内 dist，`emptyOutDir: true`，dev 用 `server.proxy` 代理 `/api` |
| B. 分离 | 后端绝对地址或同域反代前缀 | `outDir` 用默认 `dist`，dev 用 `server.proxy` 规避跨域 |

- `.env.development` / `.env.production` 管理变量，只暴露 `VITE_` 前缀；密钥禁止放前端。
- 形态 A 在 `public/` 保留 `.embedkeep`，保证 `emptyOutDir` 清空后 dist 仍非空。
- 形态 B 必须满足：后端 `allowed_origins` 含前端地址（禁用 `*`）；带 Cookie 时前端 `credentials: 'include'` 且后端不能回 `*`，两端成对出现。
- 形态 B 的 SPA 回退归静态服务器（nginx `try_files`），不归 Go；带 hash 的资源长缓存，`index.html` 必须 `no-cache`。

## 反模式

- 组件内裸写 `fetch`/`axios`，绕过 `api/` 层。
- 模块组件放全局 `components/`（违反 R13）。
- 全局组件带业务语义：`components/` 里 `import { useUserStore }` 或调 `userApi`。
- 模块间直接 import 对方组件。
- 2xx 不校验 `Content-Type` 就按 JSON 解析。
- `if (!v)` 过滤查询参数，吞掉 `0`/`false`。
- 把 `AbortError` 包装成业务错误弹提示。
- 形态 A 下 `outDir` 保持默认，导致 embed 源与产物两份不同步。
- 形态 B 下假设同源：组件里拼 `location.origin`、生产包硬编码 `localhost:8080`。
- 形态 B 下 CORS 用 `*` 又带 Cookie 凭证。
- `stores/` 塞满只被单个页面使用的状态。
- `views/` 相互 import 复用 UI（应下沉到本模块 `components/`）。
- env 变量缺 `VITE_` 前缀导致运行时 undefined。
