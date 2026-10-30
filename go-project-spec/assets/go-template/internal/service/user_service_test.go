package service_test

import (
	"context"
	"errors"
	"testing"

	"example.com/app/internal/repo"
	"example.com/app/internal/repo/repotest"
	"example.com/app/internal/service"
)

// 单轮 e2e：内存 repo → service，断言业务规则与状态流转。
// 这是性价比最高的一层测试，不需要数据库也不打桩 HTTP。
func TestUserService_CreateGetList(t *testing.T) {
	ctx := context.Background()
	mem := repotest.NewMemoryUserRepo()
	svc := service.NewUserService(mem)

	created, err := svc.Create(ctx, "  alice  ")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "alice" {
		t.Fatalf("name should be trimmed, got %q", created.Name)
	}
	if created.ID == 0 {
		t.Fatal("id should be assigned by repo")
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("get returned %q, want alice", got.Name)
	}

	if _, err := svc.Create(ctx, "bob"); err != nil {
		t.Fatalf("create second: %v", err)
	}

	page, err := svc.List(ctx, 1, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 2 || len(page.List) != 2 {
		t.Fatalf("want total=2 list=2, got total=%d list=%d", page.Total, len(page.List))
	}
	// 与真实 repo 一致：按 id 倒序
	if page.List[0].Name != "bob" {
		t.Fatalf("want newest first, got %q", page.List[0].Name)
	}
}

func TestUserService_CreateRejectsEmptyName(t *testing.T) {
	svc := service.NewUserService(repotest.NewMemoryUserRepo())
	if _, err := svc.Create(context.Background(), "   "); !errors.Is(err, service.ErrInvalidName) {
		t.Fatalf("want ErrInvalidName, got %v", err)
	}
}

func TestUserService_GetNotFound(t *testing.T) {
	svc := service.NewUserService(repotest.NewMemoryUserRepo())
	if _, err := svc.Get(context.Background(), 999); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("want repo.ErrNotFound, got %v", err)
	}
}

func TestUserService_ListNormalizesPaging(t *testing.T) {
	svc := service.NewUserService(repotest.NewMemoryUserRepo())
	// page/size 非法值应被规整为 page=1, size=10，而不是报错
	if _, err := svc.List(context.Background(), 0, 0, ""); err != nil {
		t.Fatalf("list with zero paging should be normalized, got %v", err)
	}
}
