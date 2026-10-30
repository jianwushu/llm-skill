// Package server 负责 HTTP Server 生命周期：注册路由、挂载中间件、优雅退出。
// 依赖由 main 装配后传入，本包不创建 repo / client / engine——创建即需要关闭，
// 关闭逻辑只能有一份，放在装配处（见 references/shutdown.md）。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"example.com/app/internal/config"
	"example.com/app/internal/handler"
	"example.com/app/internal/middleware"
)

// Deps 是 server 需要的外部依赖，全部由装配处构造好后传入。
// 新增一类 handler 时在此加一个字段，New 的签名不变。
// 字段为 nil 表示该能力未启用，对应路由不注册。
type Deps struct {
	Users    *handler.UserHandler
	Tasks    *handler.TaskHandler
	Frontend http.Handler
}

type Server struct {
	http *http.Server
}

// New 用已建好的依赖组装 HTTP Server：路由 → 中间件 → 监听参数。
// 不创建任何资源，因此没有需要关闭的东西；资源关闭由装配处的 closers 统一负责。
func New(cfg *config.Config, deps Deps, version string) *Server {
	mux := NewMux(deps.Users, deps.Tasks, deps.Frontend, version)
	h := middleware.Logger(middleware.Recover(middleware.CORS(cfg.HTTP.AllowedOrigins, cfg.HTTP.AllowCredentials)(mux)))

	return &Server{
		http: &http.Server{
			Addr:              cfg.HTTP.Addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
		},
	}
}

// Run 阻塞运行直到 ctx 取消，随后 10s 内优雅关闭。
// 只负责停掉自己的监听；外部资源由装配处按逆序关闭。
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", s.http.Addr)
		errCh <- s.http.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}
