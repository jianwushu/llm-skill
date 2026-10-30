// Package engine 定义多执行引擎的统一契约与调度。
//
// 工具型程序往往提供多种处理能力（文本解析、图片压缩…），每个能力实现为一个
// Engine，统一注册到 Dispatcher，由它负责：注册表、并发限流、单次超时、panic
// 隔离、耗时统计。handler 只调用 Dispatcher，禁止按引擎类型写 switch 分支。
//
// 规范（唯一来源）：references/tasks.md「请求级任务」
// 本文件即唯一实现，无副本。
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"
)

var (
	// ErrNotFound 引擎未注册。
	ErrNotFound = errors.New("engine not found")
	// ErrBusy 并发已满，应提示调用方稍后重试（映射 429）。
	ErrBusy = errors.New("engine busy, retry later")
	// ErrInvalidInput 输入不合法（缺字段、格式错误、目标被安全策略拒绝等），映射 400。
	// 各引擎的入参校验统一用它包装，避免校验失败被当成 500。
	ErrInvalidInput = errors.New("invalid input")
	// ErrDuplicateEngine 同名引擎重复注册（装配错误，必须暴露）。
	ErrDuplicateEngine = errors.New("duplicate engine name")
	// ErrInvalidEngine 引擎实现非法（nil、名称为空）。
	ErrInvalidEngine = errors.New("invalid engine")
)

// Request 一次执行请求；Payload 为各引擎自定义的输入 JSON。
type Request struct {
	Engine  string          `json:"engine"`
	Payload json.RawMessage `json:"payload"`
}

// Result 统一执行结果。
type Result struct {
	Engine  string `json:"engine"`
	Output  any    `json:"output"`
	Elapsed int64  `json:"elapsed_ms"`
}

// Engine 执行引擎契约：实现者只关心"输入 → 输出"，
// 超时、并发、panic 隔离、耗时由 Dispatcher 统一负责。
// 纯 Go 引擎与外部工具链引擎（exec.CommandContext）都实现同一接口。
type Engine interface {
	Name() string
	Description() string
	Execute(ctx context.Context, payload []byte) (any, error)
}

// Info 用于能力发现接口（GET /api/v1/engines）。
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Dispatcher 引擎调度器：注册表 + 并发信号量 + 超时 + panic 隔离。
type Dispatcher struct {
	mu      sync.RWMutex
	engines map[string]Engine
	sem     chan struct{}
	timeout time.Duration
}

func NewDispatcher(maxConcurrent int, timeout time.Duration) *Dispatcher {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Dispatcher{
		engines: make(map[string]Engine),
		sem:     make(chan struct{}, maxConcurrent),
		timeout: timeout,
	}
}

// Register 注册引擎（装配期调用，不并发注册）。
//
// 与"同名静默覆盖"不同，重复名称视为装配错误：生产环境里两个能力撞名会让后者
// 悄悄顶掉前者，前端看到的是错乱的行为。因此本方法：
//   - nil 引擎、空名称 → ErrInvalidEngine
//   - 名称重复（含本次调用内重复、与已注册重复）→ ErrDuplicateEngine
//
// 同一批注册是全有或全无：任何一项校验失败都不会留下部分注册状态。
// 测试替身或运行时热替换请显式使用 Replace。
func (d *Dispatcher) Register(engines ...Engine) error {
	names := make([]string, 0, len(engines))
	seen := make(map[string]struct{}, len(engines))
	for _, e := range engines {
		if isNilEngine(e) {
			return fmt.Errorf("%w: nil engine", ErrInvalidEngine)
		}
		name := e.Name()
		if name == "" {
			return fmt.Errorf("%w: engine name is required", ErrInvalidEngine)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%w: %q registered twice in one call", ErrDuplicateEngine, name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range names {
		if _, exists := d.engines[name]; exists {
			return fmt.Errorf("%w: %q already registered (use Replace for explicit override)", ErrDuplicateEngine, name)
		}
	}
	for i, e := range engines {
		d.engines[names[i]] = e
	}
	return nil
}

// Replace 显式替换同名引擎。仅用于测试替身或明确的运行时热替换；
// 生产装配路径一律用 Register，让撞名在启动期就失败。
func (d *Dispatcher) Replace(e Engine) error {
	if isNilEngine(e) {
		return fmt.Errorf("%w: nil engine", ErrInvalidEngine)
	}
	name := e.Name()
	if name == "" {
		return fmt.Errorf("%w: engine name is required", ErrInvalidEngine)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.engines[name] = e
	return nil
}

// Len 已注册引擎数量，供装配期完整性检查（例如要求至少注册一个能力）。
func (d *Dispatcher) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.engines)
}

// List 返回已注册引擎清单（按名称排序，便于前端稳定渲染）。
func (d *Dispatcher) List() []Info {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Info, 0, len(d.engines))
	for _, e := range d.engines {
		out = append(out, Info{Name: e.Name(), Description: e.Description()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run 调度一次执行：
//  1. 查找引擎（不存在 → ErrNotFound）
//  2. 获取并发令牌（满 → ErrBusy；不排队，避免请求堆积）
//  3. 施加单次执行超时
//  4. panic 隔离后执行并统计耗时
func (d *Dispatcher) Run(ctx context.Context, req Request) (Result, error) {
	d.mu.RLock()
	e, ok := d.engines[req.Engine]
	d.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrNotFound, req.Engine)
	}

	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
		return Result{}, ErrBusy
	}

	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	start := time.Now()
	out, err := executeSafely(runCtx, e, req.Payload)
	if err != nil {
		return Result{}, err
	}
	return Result{Engine: e.Name(), Output: out, Elapsed: time.Since(start).Milliseconds()}, nil
}

// executeSafely 隔离引擎 panic，单个引擎崩溃不影响进程。
func executeSafely(ctx context.Context, e Engine, payload []byte) (out any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("engine %q panicked: %v", e.Name(), rec)
		}
	}()
	return e.Execute(ctx, payload)
}

// isNilEngine 识别 typed-nil（如 (*Foo)(nil) 赋给 Engine 接口），
// 否则注册成功但调用时 panic。
func isNilEngine(e Engine) bool {
	if e == nil {
		return true
	}
	v := reflect.ValueOf(e)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return v.IsNil()
	}
	return false
}
