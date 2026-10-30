// Package service 是业务逻辑层：业务规则与事务编排。
// 依赖方向：handler → service → repo；禁止 import handler，禁止拼 SQL。
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"example.com/app/internal/model"
	"example.com/app/internal/repo"
)

// 业务错误：handler 负责映射到 HTTP 状态码与业务 code。
var (
	ErrInvalidName = errors.New("name must not be empty")
)

type UserService struct {
	users repo.UserRepo
}

func NewUserService(users repo.UserRepo) *UserService {
	return &UserService{users: users}
}

func (s *UserService) List(ctx context.Context, page, size int, keyword string) (model.Page[model.User], error) {
	page, size = normalizePage(page, size)
	return s.users.List(ctx, strings.TrimSpace(keyword), size, (page-1)*size)
}

func (s *UserService) Get(ctx context.Context, id int64) (model.User, error) {
	return s.users.Get(ctx, id)
}

func (s *UserService) Create(ctx context.Context, name string) (model.User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.User{}, ErrInvalidName
	}
	u := model.User{Name: name, CreatedAt: time.Now()}
	if err := s.users.Create(ctx, &u); err != nil {
		return model.User{}, err
	}
	return u, nil
}

func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	return page, size
}
