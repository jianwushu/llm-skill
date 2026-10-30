// Package repotest 提供 repo 接口的内存实现，**仅供测试引用**。
//
// 规则（见 references/database.md「测试替身」）：
//   - 生产代码禁止 import 本包（会拖入测试依赖、增大二进制）；
//   - 实现必须与真实 repo 语义一致（排序、过滤、未找到错误），否则测试通过但线上错。
package repotest

import (
	"context"
	"sort"
	"strings"
	"sync"

	"example.com/app/internal/model"
	"example.com/app/internal/repo"
)

// MemoryUserRepo 是 repo.UserRepo 的内存实现，可用于 service 层单测与单轮 e2e。
type MemoryUserRepo struct {
	mu    sync.Mutex
	seq   int64
	users []model.User
}

// 编译期确认实现了接口，避免接口变更后测试替身悄悄失效。
var _ repo.UserRepo = (*MemoryUserRepo)(nil)

// NewMemoryUserRepo 创建空仓库。
func NewMemoryUserRepo() *MemoryUserRepo {
	return &MemoryUserRepo{users: make([]model.User, 0, 8)}
}

// List 返回按 id 倒序的过滤结果，与真实 repo 的 ORDER BY id DESC 保持一致。
func (r *MemoryUserRepo) List(_ context.Context, keyword string, limit, offset int) (model.Page[model.User], error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	matched := make([]model.User, 0, len(r.users))
	kw := strings.ToLower(strings.TrimSpace(keyword))
	for _, u := range r.users {
		if kw == "" || strings.Contains(strings.ToLower(u.Name), kw) {
			matched = append(matched, u)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })

	total := int64(len(matched))
	if offset >= len(matched) {
		return model.Page[model.User]{Total: total, List: []model.User{}}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(matched) {
		end = len(matched)
	}
	return model.Page[model.User]{Total: total, List: matched[offset:end]}, nil
}

// Get 未找到时返回 repo.ErrNotFound，与真实实现一致。
func (r *MemoryUserRepo) Get(_ context.Context, id int64) (model.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return model.User{}, repo.ErrNotFound
}

// Create 分配自增 id 并回写到入参，模拟 INSERT ... RETURNING id。
func (r *MemoryUserRepo) Create(_ context.Context, u *model.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	u.ID = r.seq
	r.users = append(r.users, *u)
	return nil
}

// Len 返回当前记录数，便于测试断言。
func (r *MemoryUserRepo) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.users)
}
