// Package model 存放领域实体（纯结构体），各层均可引用；
// 禁止包含 IO、HTTP、数据库框架代码。
package model

import "time"

type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Page 通用分页包装，repo/service/handler 复用。
type Page[T any] struct {
	Total int64 `json:"total"`
	List  []T   `json:"list"`
}
