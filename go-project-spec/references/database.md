# 数据库

## 驱动：纯 Go，按需引入

| 场景 | 驱动 | 注册名 | 说明 |
|---|---|---|---|
| 单机嵌入式元数据 | `modernc.org/sqlite` | `sqlite` | 默认首选，零外部依赖，可交叉编译 |
| 已有 PG 基础设施 | `jackc/pgx/v5/stdlib` | `pgx` | 走 `database/sql` 接口 |
| 已有 Oracle 基础设施 | `sijms/go-ora/v2` | `oracle` | 纯 Go，无需 OCI 客户端 |

**禁止** `mattn/go-sqlite3`、`godror` 等 CGO/OCI 驱动：引入后 `CGO_ENABLED=0` 立即失效，交付形态被锁死在构建机上。

**不要同时 import 三个驱动**，各带一堆依赖，二进制白白大 20MB+。只留当前项目用的那一行；其余注释掉并注明"需要时取消注释"。

## 条款

1. **唯一建库点**：一个函数按配置选驱动、拼 DSN、设连接池、`Ping()` 后返回 `*sql.DB`。其他地方禁止 `sql.Open`。
2. **连接池显式设置**：`MaxOpenConns` / `MaxIdleConns` / `ConnMaxLifetime` 都要有值，不靠默认（默认 `MaxOpenConns` 无上限）。SQLite 写并发靠 WAL + `busy_timeout`，高写场景把 `MaxOpenConns` 压到 1 最稳。
3. **方言差异只在 repo 内**：占位符、分页、时间函数、自增、布尔全部在数据访问层消化，上层无感知。
4. **单库项目不做方言抽象**：只有一种库就直接写该方言 SQL。多写一层 `if driver == ...` 是纯负担。
5. **事务边界在业务层，事务对象向下传**：业务层决定"这几步要原子"，通过 `DBTX` 接口（`*sql.DB` 与 `*sql.Tx` 的公共子集）传给 repo；repo 不自开事务。
6. **迁移可重复执行**：`schema_version` 表 + 顺序 SQL，启动时补齐；或引 `golang-migrate`。禁止手工改表再靠记忆同步。
7. **数据库可选**：`driver: none` 时不打开连接，`repo` 返回 `nil`，上层判空并返回明确错误，主功能照常——不能因为没配库就 panic 或 `os.Exit`。

## 方言对照（多库项目参考，单库跳过）

| 差异点 | SQLite | PostgreSQL | Oracle |
|---|---|---|---|
| 占位符 | `?` | `$1,$2…` | `:1,:2…` |
| 分页 | `LIMIT ? OFFSET ?` | `LIMIT $n OFFSET $m` | `OFFSET :n ROWS FETCH NEXT :m ROWS ONLY` |
| 自增主键 | `INTEGER PRIMARY KEY AUTOINCREMENT` | `BIGSERIAL` / `GENERATED` | 序列 + 触发器或 `IDENTITY` |
| 当前时间 | `datetime('now')` | `now()` | `SYSDATE` / `SYSTIMESTAMP` |
| 大小写 | 不敏感 | 小写敏感 | 默认转大写 |
| 布尔 | `INTEGER 0/1` | `boolean` | `NUMBER(1)` |
| 抢批 | 条件 `UPDATE` + 查 `RowsAffected` | `FOR UPDATE SKIP LOCKED` | `FOR UPDATE SKIP LOCKED` |
| 空串 vs NULL | 空串是空串 | 空串是空串 | **空串即 NULL** |

## SQLite 两个必踩的坑

1. **父目录不存在报 `unable to open database file (14)`**：驱动不会自动建目录，建库前 `os.MkdirAll(filepath.Dir(path))`。模板 `repo.Open` 已内含这一步。
2. **模块路径是 `modernc.org/sqlite`**，不是 `github.com/modernc.org/sqlite`；写错时 `go mod tidy` 报找不到模块。

## 测试替身

- 接口的内存实现放 `repo/repotest/`，**仅测试 import**，不进生产二进制。
- 内存实现要复刻最容易出错的语义：唯一约束冲突、更新不存在行返回 `sql.ErrNoRows`、分页边界。
- 单轮端到端（内存 repo + fake 上游 → 跑一轮 → 断言状态流转）性价比最高，秒级覆盖状态机、重试、跳过语义。

## 反模式

| 反模式 | 后果 | 正确做法 |
|---|---|---|
| CGO 驱动 | 交叉编译失效 | 纯 Go 驱动 |
| 同时 import 多驱动 | 二进制膨胀、依赖冲突 | 只留当前用的 |
| 全局 `var DB *sql.DB` | 无法测试、并发隐患 | 构造注入 |
| repo 里自开事务 | 事务边界散落、无法组合 | 业务层开事务，向下传 `DBTX` |
| `SELECT *` | 加字段即破坏扫描 | 显式列名 |
| 迁移靠手工执行 | 环境间结构漂移 | 启动时幂等迁移 |
| 生产 import `repotest` | 依赖污染 | 仅测试引用 |
| 没配库就 `os.Exit` | 可选依赖被当必需 | `driver: none` + 上层判空 |

## 校验

```bash
go build ./...                  # CGO_ENABLED=0 下必须通过
CGO_ENABLED=0 go test ./...     # 确认没有隐式 CGO 依赖
# 运行期：driver=none 可跑；sqlite 下 CRUD 正常；删掉 DSN 父目录后重启 → 自动创建
```
