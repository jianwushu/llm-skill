module example.com/app

go 1.22

// 首次使用：全局替换 module 名后执行 `go mod tidy`。
//
// 依赖按需引入（不要在 db.go 里同时 import 三个驱动，会让二进制约增大 20MB）：
//   - SQLite     modernc.org/sqlite（默认启用）
//   - PostgreSQL github.com/jackc/pgx/v5/stdlib（需要时取消注释）
//   - Oracle     github.com/sijms/go-ora/v2（需要时取消注释）
//
// 驱动均为纯 Go，支持 CGO_ENABLED=0 交叉编译；禁止替换为 CGO/OCI 驱动。
