// Package logger 统一日志初始化：
//   - 级别由配置控制（debug|info|warn|error），非法值启动即失败
//   - 同时输出到控制台与按天滚动的日志文件（app-YYYY-MM-DD.log，默认轮转）
//   - 提供过期日志文件定时清理（保留天数、扫描频率可配置）
//
// 规范（唯一来源）：references/config-logging.md「日志」
// 本文件即唯一实现，无副本。
//
// 组件不依赖项目的 config 结构，由装配处把配置映射成 Options，
// 这样同一份实现可以在 CLI、服务、工具程序中复用。
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	filePrefix   = "app-"
	fileExt      = ".log"
	dayLayout    = "2006-01-02"
	defaultDir   = "logs"
	defaultLevel = "info"
	// DefaultRetentionDays 日志默认保留天数。
	DefaultRetentionDays = 7
	// DefaultCleanIntervalH 过期清理的默认扫描间隔（小时），频率不必太高。
	DefaultCleanIntervalH = 24
)

// Options 日志参数。
type Options struct {
	Dir            string // 日志目录
	Level          string // debug|info|warn|error
	RetentionDays  int    // 保留天数，<=0 取默认 7
	CleanIntervalH int    // 清理扫描间隔（小时），<=0 取默认 24
}

func (o Options) withDefaults() Options {
	if o.Dir == "" {
		o.Dir = defaultDir
	}
	if o.Level == "" {
		o.Level = defaultLevel
	}
	if o.RetentionDays <= 0 {
		o.RetentionDays = DefaultRetentionDays
	}
	if o.CleanIntervalH <= 0 {
		o.CleanIntervalH = DefaultCleanIntervalH
	}
	return o
}

// dailyWriter 按天切换日志文件，配合清理任务实现过期回收。
type dailyWriter struct {
	mu  sync.Mutex
	dir string
	day string
	f   *os.File
}

func newDailyWriter(dir string) *dailyWriter {
	return &dailyWriter{dir: dir}
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	today := time.Now().Format(dayLayout)
	if w.f == nil || today != w.day {
		if err := w.rotateLocked(today); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

func (w *dailyWriter) rotateLocked(day string) error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(w.dir, filePrefix+day+fileExt), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	w.f, w.day = f, day
	return nil
}

// Close 关闭当前日志文件，进程退出前调用。
func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Init 初始化全局 slog：级别由配置控制，输出到控制台 + 按天滚动日志文件。
// 返回关闭函数，调用方在退出时执行（必须最后关闭，保证其他组件退出日志能落盘）。
func Init(opt Options) (func(), error) {
	opt = opt.withDefaults()
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(opt.Level))); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", opt.Level, err)
	}

	dw := newDailyWriter(opt.Dir)
	slog.SetDefault(slog.New(slog.NewTextHandler(
		io.MultiWriter(os.Stdout, dw),
		&slog.HandlerOptions{Level: level},
	)))
	return func() { _ = dw.Close() }, nil
}

// StartCleaner 启动日志过期清理：启动时执行一次，之后每 CleanIntervalH 小时扫描一次。
// 只删除匹配 app-YYYY-MM-DD.log 且早于 RetentionDays 的文件，其他文件不触碰。
func StartCleaner(ctx context.Context, opt Options) {
	opt = opt.withDefaults()
	interval := time.Duration(opt.CleanIntervalH) * time.Hour
	go func() {
		cleanExpired(opt.Dir, opt.RetentionDays)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanExpired(opt.Dir, opt.RetentionDays)
			}
		}
	}()
}

func cleanExpired(dir string, retentionDays int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // 目录不存在等场景无需清理
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileExt) {
			continue
		}
		day, err := time.Parse(dayLayout, strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileExt))
		if err != nil {
			continue // 非日期命名，跳过
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
