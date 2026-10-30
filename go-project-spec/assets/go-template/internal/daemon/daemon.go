// Package daemon 是常驻链路（轮询/批次抢占/断点续跑型任务）的运维契约与注册表。
//
// 适用：任务源在数据库/队列/落地表，处理时长不可控，需要重试、断点、多实例竞争消费。
// 不适用：请求进来秒级返回的同步能力——那类请用 internal/engine（见 references/tasks.md）。
//
// 设计要点（详见 references/tasks.md「常驻链路」）：
//   - 所有链路实现同一个 Daemon 接口，运维 HTTP 接口由注册表遍历生成，不写按 module 的 switch；
//   - Pause 只停止拉新，在途任务继续跑完；停机时先 Pause → 等在途排空 → 再 Stop；
//   - 状态机五态（pending/processing/completed/skipped/failed）由链路实现，
//     skipped 与 failed 语义不可混用。
//
// 若项目没有常驻链路需求，删除本包及装配处的引用即可。
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State 链路运行状态。
type State string

const (
	StateRunning State = "running"
	StatePaused  State = "paused"
	StateStopped State = "stopped"
)

// DefaultDrainTimeout 停机时等待在途任务结束的默认上限。
const DefaultDrainTimeout = 30 * time.Second

// drainPollInterval 排空检测的轮询间隔。
const drainPollInterval = 100 * time.Millisecond

// StatusDTO 链路运行态快照，供运维接口直接序列化返回。
type StatusDTO struct {
	Module       string    `json:"module"`
	State        State     `json:"state"`
	InFlight     int64     `json:"in_flight"`
	ActiveWorker int       `json:"active_worker"`
	QueueDepth   int64     `json:"queue_depth"`
	PollInterval int       `json:"poll_interval_s"`
	BatchSize    int       `json:"batch_size"`
	LastPollAt   time.Time `json:"last_poll_at"`
	LastError    string    `json:"last_error,omitempty"`
}

// ProgressDTO 五态计数。注意：默认实现是内存计数，进程重启归零，
// 前端必须标注「本次运行」，不能当历史累计。
type ProgressDTO struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Completed  int64 `json:"completed"`
	Skipped    int64 `json:"skipped"`
	Failed     int64 `json:"failed"`
}

// Daemon 常驻链路契约。运维接口只依赖本接口，不依赖具体链路实现。
type Daemon interface {
	Module() string                                                    // 链路标识：parse / image / sync...
	GetState() State                                                   // running | paused | stopped
	GetStatus() StatusDTO                                              // 在途数/活跃 worker/队列/轮询间隔/批量/最近轮询时间
	GetProgress() ProgressDTO                                          // 五态计数
	Pause()                                                            // 只停止拉新，在途继续跑完
	Resume()                                                           // 恢复拉新
	ResetStale(ctx context.Context, timeoutMinutes int) (int64, error) // 回收租约超时记录
	InFlight() int64                                                   // 在途任务数，供优雅停机与在线更新判断
}

// Unit 一条链路的装配单元：契约 + 生命周期钩子。
// Start 应当非阻塞（内部起 goroutine）或受 ctx 控制；Stop 必须幂等。
type Unit struct {
	Module string
	Handle Daemon
	Start  func(ctx context.Context) error
	Stop   func() error
}

// Registry 链路注册表。装配层一次性注册，运维路由与生命周期都只遍历这里。
type Registry struct {
	mu    sync.RWMutex
	units []Unit
	byID  map[string]Unit
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]Unit)}
}

// Register 注册一条链路。Module 为空或重复时返回错误。
func (r *Registry) Register(u Unit) error {
	if u.Module == "" {
		return errors.New("daemon: module name is required")
	}
	if u.Handle == nil {
		return fmt.Errorf("daemon %s: handle is required", u.Module)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[u.Module]; exists {
		return fmt.Errorf("daemon: duplicate module %q", u.Module)
	}
	r.byID[u.Module] = u
	r.units = append(r.units, u)
	return nil
}

// All 返回按 Module 排序的链路副本，保证运维接口输出稳定。
func (r *Registry) All() []Unit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Unit, len(r.units))
	copy(out, r.units)
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// Get 按 module 取链路。
func (r *Registry) Get(module string) (Daemon, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.byID[module]
	if !ok {
		return nil, false
	}
	return u.Handle, true
}

// Statuses 汇总全部链路状态，用于 GET /api/v1/daemons 总览。
func (r *Registry) Statuses() []StatusDTO {
	units := r.All()
	out := make([]StatusDTO, 0, len(units))
	for _, u := range units {
		out = append(out, u.Handle.GetStatus())
	}
	return out
}

// StartAll 启动全部链路；任一失败即返回错误（由装配层决定是否回滚）。
func (r *Registry) StartAll(ctx context.Context) error {
	for _, u := range r.All() {
		if u.Start == nil {
			continue
		}
		if err := u.Start(ctx); err != nil {
			return fmt.Errorf("start daemon %s: %w", u.Module, err)
		}
	}
	return nil
}

// StopAll 优雅停机：先全部 Pause，再等在途排空（上限 DefaultDrainTimeout），
// 最后逆序 Stop 并聚合错误。重复调用安全。
func (r *Registry) StopAll() error {
	return r.StopAllWithDrain(DefaultDrainTimeout)
}

// StopAllWithDrain 同 StopAll，可自定义排空等待上限。
func (r *Registry) StopAllWithDrain(drain time.Duration) error {
	units := r.All()
	if len(units) == 0 {
		return nil
	}

	// 1. 只停拉新，在途继续
	for _, u := range units {
		u.Handle.Pause()
	}

	// 2. 等在途排空（超时则强制继续，避免停机卡死）
	deadline := time.Now().Add(drain)
	for {
		var total int64
		for _, u := range units {
			total += u.Handle.InFlight()
		}
		if total == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(drainPollInterval)
	}

	// 3. 逆序 Stop，聚合错误
	var errs []error
	for i := len(units) - 1; i >= 0; i-- {
		if units[i].Stop == nil {
			continue
		}
		if err := units[i].Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop daemon %s: %w", units[i].Module, err))
		}
	}
	return errors.Join(errs...)
}
