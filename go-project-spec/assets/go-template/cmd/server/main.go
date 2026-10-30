// Package main 是程序唯一入口：只做装配（配置 → 日志 → 数据库 → 客户端 → service/handler → server），
// 禁止出现业务逻辑与 SQL。
//
// 装配遵循三条约束（见 references/shutdown.md）：
//  1. 任一环节失败必须回滚已建资源（不泄漏连接与文件句柄）；
//  2. 可选依赖失败不阻断启动（数据库/外部客户端可按项目需求降级）；
//  3. 关闭顺序与创建顺序相反：HTTP Server → 外部客户端 → DB → 日志（日志最后关）。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/app/internal/client"
	"example.com/app/internal/config"
	"example.com/app/internal/engine"
	"example.com/app/internal/handler"
	"example.com/app/internal/logger"
	"example.com/app/internal/repo"
	"example.com/app/internal/server"
	"example.com/app/internal/service"
	"example.com/app/internal/webfs"
)

// version 可通过 -ldflags "-X main.version=v1.0.0" 注入，经 /api/v1/health 暴露。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx)
	switch {
	case err == nil:
		slog.Info("server stopped gracefully", "version", version)
	case errors.Is(err, config.ErrConfigFileCreated):
		// 首次启动：默认配置文件已生成，提示后正常退出
		fmt.Printf("未找到配置文件，已生成默认配置文件: %s\n", config.DefaultConfigPath)
		fmt.Println("请按需修改配置后重新启动程序。")
	default:
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

// run 完成全部装配并阻塞运行。
// 返回 error 表示装配失败或运行异常；返回 nil 表示正常退出。
func run(ctx context.Context) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// 配置校验先于日志初始化：配置类错误应报成配置错误，而非日志初始化失败
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// 按创建顺序登记释放函数，退出时逆序执行。
	// 装配中途失败（ok=false）与正常运行结束使用同一套关闭逻辑，避免遗漏。
	ok := false
	var closers []func() error
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			if cerr := closers[i](); cerr != nil {
				if ok {
					slog.Warn("close resource failed", "err", cerr)
				} else {
					fmt.Fprintf(os.Stderr, "rollback close failed: %v\n", cerr)
				}
			}
		}
	}()

	// 日志：级别可配、控制台 + 按天滚动文件、过期自动清理。
	// 顺序在配置加载与校验之后：配置类错误应报成配置错误，而不是日志初始化失败。
	// 但日志必须先于其他组件——后续组件的初始化日志都要落盘。
	logOpt := logger.Options{
		Dir:            cfg.Log.Dir,
		Level:          cfg.Log.Level,
		RetentionDays:  cfg.Log.RetentionDays,
		CleanIntervalH: cfg.Log.CleanIntervalH,
	}
	closeLog, err := logger.Init(logOpt)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	closers = append(closers, func() error { closeLog(); return nil })
	logger.StartCleaner(ctx, logOpt)

	// 数据库是可选组件：driver=none 时不建连接，程序以无持久化模式运行
	var db *sql.DB
	if cfg.Database.Enabled() {
		db, err = repo.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		closers = append(closers, db.Close)
	} else {
		slog.Warn("database disabled, running without persistence", "driver", cfg.Database.Driver)
	}

	// 业务三层：repo → service → handler。数据库未启用时整条跳过，对应路由不注册。
	var userHandler *handler.UserHandler
	if db != nil {
		users := repo.NewUserRepo(db, cfg.Database.Driver)
		userHandler = handler.NewUserHandler(service.NewUserService(users))
	}

	// 外部客户端：全项目唯一构造点，禁止在 service/handler 内 new。
	// 出站目标校验策略由配置决定——URL 来自用户输入时用 deny_private，
	// 可信内网上游用 allow_private / allow_hosts（见 references/integration.md「目标校验」）。
	guard, err := client.NewGuard(client.SSRFPolicy(cfg.Client.SSRFPolicy), cfg.Client.SSRFAllowHosts)
	if err != nil {
		return fmt.Errorf("init ssrf guard: %w", err)
	}
	httpClient := client.New(client.Options{
		Timeout: time.Duration(cfg.Client.HTTPTimeoutSec) * time.Second,
		Retries: cfg.Client.HTTPRetries,
		Guard:   guard,
	})
	closers = append(closers, func() error { httpClient.CloseIdleConnections(); return nil })

	// 执行引擎注册表：新增能力只需在此 Register，handler/路由无需改动。
	// 重复注册会返回错误——装配期就暴露撞名，而不是让后注册的静默顶掉前一个。
	dispatcher := engine.NewDispatcher(
		cfg.Engine.MaxConcurrent,
		time.Duration(cfg.Engine.TimeoutSec)*time.Second,
	)
	if err := dispatcher.Register(
		engine.TextStatsEngine{},
		engine.ImageCompressEngine{},
		engine.NewHTTPProbeEngine(httpClient, guard),
	); err != nil {
		return fmt.Errorf("register engines: %w", err)
	}
	taskHandler := handler.NewTaskHandler(dispatcher)

	// 前端托管形态：
	//   embed 形态（EmbedFrontend=true）→ 挂载内嵌产物，未命中走 SPA 回退；
	//   前后端分离形态（false）→ 不挂载，未匹配路径返回 JSON 404。
	var frontend http.Handler
	if cfg.HTTP.EmbedFrontend {
		fh, err := webfs.Handler()
		if err != nil {
			return fmt.Errorf("load embedded frontend: %w", err)
		}
		frontend = fh
	}

	// server 只负责路由、中间件与生命周期，不创建任何需要关闭的资源。
	srv := server.New(cfg, server.Deps{
		Users:    userHandler,
		Tasks:    taskHandler,
		Frontend: frontend,
	}, version)

	ok = true // 装配完成
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("server exited: %w", err)
	}
	return nil
}
