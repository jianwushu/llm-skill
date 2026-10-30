package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"example.com/app/internal/config"
	"example.com/app/internal/model"
)

// ErrNotFound 通用未找到错误，service/handler 据此映射 404。
var ErrNotFound = errors.New("record not found")

// UserRepo 接口先行，service 只依赖本接口，便于测试替身。
type UserRepo interface {
	List(ctx context.Context, keyword string, limit, offset int) (model.Page[model.User], error)
	Get(ctx context.Context, id int64) (model.User, error)
	Create(ctx context.Context, u *model.User) error
}

type userRepo struct {
	db     *sql.DB
	driver string
}

func NewUserRepo(db *sql.DB, driver string) UserRepo {
	return &userRepo{db: db, driver: driver}
}

// ph 返回第 n 个占位符 —— 方言差异收敛点之一。
func (r *userRepo) ph(n int) string {
	switch r.driver {
	case config.DriverPostgres:
		return fmt.Sprintf("$%d", n)
	case config.DriverOracle:
		return fmt.Sprintf(":%d", n)
	default: // sqlite
		return "?"
	}
}

// limitOffset 分页方言 —— 方言差异收敛点之二。
func (r *userRepo) limitOffset(limit, offset int) string {
	switch r.driver {
	case config.DriverOracle:
		return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	default: // sqlite / postgres
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
}

func (r *userRepo) List(ctx context.Context, keyword string, limit, offset int) (model.Page[model.User], error) {
	where := ""
	args := []any{}
	if keyword != "" {
		where = fmt.Sprintf(" WHERE name LIKE %s", r.ph(len(args)+1))
		args = append(args, "%"+keyword+"%")
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users"+where, args...).Scan(&total); err != nil {
		return model.Page[model.User]{}, fmt.Errorf("count users: %w", err)
	}

	q := fmt.Sprintf("SELECT id, name, created_at FROM users%s ORDER BY id DESC %s", where, r.limitOffset(limit, offset))
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return model.Page[model.User]{}, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	page := model.Page[model.User]{List: []model.User{}, Total: total}
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Name, &u.CreatedAt); err != nil {
			return model.Page[model.User]{}, fmt.Errorf("scan user: %w", err)
		}
		page.List = append(page.List, u)
	}
	return page, rows.Err()
}

func (r *userRepo) Get(ctx context.Context, id int64) (model.User, error) {
	q := fmt.Sprintf("SELECT id, name, created_at FROM users WHERE id = %s", r.ph(1))
	var u model.User
	err := r.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Name, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

func (r *userRepo) Create(ctx context.Context, u *model.User) error {
	q := fmt.Sprintf("INSERT INTO users (name, created_at) VALUES (%s, %s)", r.ph(1), r.ph(2))
	switch r.driver {
	case config.DriverOracle:
		// go-ora 不支持 LastInsertId；生产中改用 RETURNING id INTO 或查询序列 CURRVAL。
		if _, err := r.db.ExecContext(ctx, q, u.Name, u.CreatedAt); err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		return nil
	default: // sqlite / postgres 支持 RETURNING
		return r.db.QueryRowContext(ctx, q+" RETURNING id", u.Name, u.CreatedAt).Scan(&u.ID)
	}
}
