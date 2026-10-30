package repo

import (
	"database/sql"
	"fmt"

	"example.com/app/internal/config"
)

// schemas 各数据库的基础建表语句（CREATE IF NOT EXISTS / 幂等）。
// 适合小型应用的启动时自动迁移；表结构演进后，建议升级为按版本号的
// 顺序迁移（schema_version 表 + SQL 文件，分数据库目录存放），
// 详见 references/database.md「方言对照」。
var schemas = map[string][]string{
	config.DriverSQLite: {
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
	},
	config.DriverPostgres: {
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	},
	config.DriverOracle: {
		`CREATE TABLE users (
			id NUMBER(19) GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name VARCHAR2(255) NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
	},
}

// Migrate 启动时执行幂等建表。失败直接中断启动（fail fast）。
func Migrate(db *sql.DB, driver string) error {
	stmts, ok := schemas[driver]
	if !ok {
		return fmt.Errorf("no schema for driver %q", driver)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("migrate (%s): %w", driver, err)
		}
	}
	return nil
}
