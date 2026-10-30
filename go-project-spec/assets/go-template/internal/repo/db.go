// Package repo 是数据访问层：接口定义 + 多数据库实现。
// 依赖方向：只被 service 消费，禁止 import service/handler。
// 数据库方言差异（占位符、分页、自增）只允许收敛在本层。
package repo

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"example.com/app/internal/config"

	// 按需保留驱动注册：只保留当前项目实际使用的驱动，删掉其余几行，
	// 避免拖入无用依赖（三个驱动同时 import 会让二进制约增大 20MB）。
	// 严禁替换为 CGO/OCI 驱动（mattn/go-sqlite3、godror），否则 CGO_ENABLED=0 交叉编译失效。
	_ "modernc.org/sqlite" // SQLite
	// _ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL：需要时取消注释并 go mod tidy
	// _ "github.com/sijms/go-ora/v2"     // Oracle：需要时取消注释并 go mod tidy
)

// Open 是全项目唯一的建库点：按配置选择驱动，返回 *sql.DB。
// 数据库是可选组件：driver=none 时调用方不应调用本函数（config.Database.Enabled 为 false）。
func Open(cfg config.DatabaseConfig) (*sql.DB, error) {
	if cfg.Driver == "" || cfg.Driver == config.DriverNone {
		return nil, fmt.Errorf("database driver is %q: 该配置下不应打开数据库连接", cfg.Driver)
	}
	if !slices.Contains(sql.Drivers(), cfg.Driver) {
		return nil, fmt.Errorf("database driver %q 未注册：请在 internal/repo/db.go 中取消对应驱动的 import 并 go mod tidy", cfg.Driver)
	}

	if cfg.Driver == config.DriverSQLite {
		// SQLite 不会自动创建父目录，缺失时报 "unable to open database file"
		if err := ensureSQLiteDir(cfg.DSN); err != nil {
			return nil, fmt.Errorf("prepare sqlite data dir: %w", err)
		}
	}

	db, err := sql.Open(cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSec) * time.Second)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database (%s): %w", cfg.Driver, err)
	}

	// 启动时幂等建表（fail fast）
	if err := Migrate(db, cfg.Driver); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// ensureSQLiteDir 从 DSN 中提取文件路径并创建父目录。
// 兼容 file:data/app.db?_pragma=... 与 data/app.db 两种写法。
func ensureSQLiteDir(dsn string) error {
	p := dsn
	if i := strings.Index(p, "file:"); i >= 0 {
		p = p[i+len("file:"):]
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if p == "" || p == ":memory:" {
		return nil
	}
	return os.MkdirAll(filepath.Dir(p), 0o755)
}
