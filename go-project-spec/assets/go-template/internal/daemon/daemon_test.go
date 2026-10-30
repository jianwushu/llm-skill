package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDaemon 用于验证注册表与停机流程，不涉及真实任务源。
type fakeDaemon struct {
	module   string
	state    atomic.Value // State
	inFlight atomic.Int64
	started  atomic.Int32
	stopped  atomic.Int32
	failStop bool

	mu  sync.Mutex
	log []string
}

func newFake(m string) *fakeDaemon {
	f := &fakeDaemon{module: m}
	f.state.Store(StateStopped)
	return f
}

func (f *fakeDaemon) Module() string  { return f.module }
func (f *fakeDaemon) GetState() State { return f.state.Load().(State) }
func (f *fakeDaemon) InFlight() int64 { return f.inFlight.Load() }
func (f *fakeDaemon) GetProgress() ProgressDTO {
	return ProgressDTO{Completed: int64(f.stopped.Load())}
}

func (f *fakeDaemon) GetStatus() StatusDTO {
	return StatusDTO{Module: f.module, State: f.GetState(), InFlight: f.InFlight(), BatchSize: 50}
}

func (f *fakeDaemon) Pause() {
	f.state.Store(StatePaused)
	f.mu.Lock()
	f.log = append(f.log, "pause")
	f.mu.Unlock()
}

func (f *fakeDaemon) Resume() { f.state.Store(StateRunning) }

func (f *fakeDaemon) ResetStale(_ context.Context, _ int) (int64, error) { return 0, nil }

func (f *fakeDaemon) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.log))
	copy(out, f.log)
	return out
}

func TestRegisterValidation(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Unit{Module: "", Handle: newFake("x")}); err == nil {
		t.Fatal("empty module should be rejected")
	}
	if err := reg.Register(Unit{Module: "parse"}); err == nil {
		t.Fatal("nil handle should be rejected")
	}
	if err := reg.Register(Unit{Module: "parse", Handle: newFake("parse")}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(Unit{Module: "parse", Handle: newFake("parse")}); err == nil {
		t.Fatal("duplicate module should be rejected")
	}
}

func TestAllIsSortedAndIndependent(t *testing.T) {
	reg := NewRegistry()
	for _, m := range []string{"sync", "parse", "image"} {
		if err := reg.Register(Unit{Module: m, Handle: newFake(m)}); err != nil {
			t.Fatalf("register %s: %v", m, err)
		}
	}
	all := reg.All()
	if len(all) != 3 || all[0].Module != "image" || all[2].Module != "sync" {
		t.Fatalf("All should be sorted by module, got %+v", all)
	}
	// 返回副本：调用方修改不影响注册表
	all[0].Module = "hacked"
	if reg.All()[0].Module != "image" {
		t.Fatal("All must return a copy")
	}
}

func TestStopAllPausesThenDrainsThenStops(t *testing.T) {
	reg := NewRegistry()
	f := newFake("parse")
	f.state.Store(StateRunning)

	var stopOrder []string
	if err := reg.Register(Unit{
		Module: "parse",
		Handle: f,
		Start:  func(context.Context) error { f.started.Add(1); return nil },
		Stop:   func() error { stopOrder = append(stopOrder, "parse"); return nil },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := reg.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}
	if f.started.Load() != 1 {
		t.Fatal("start hook should be called")
	}

	// 模拟在途任务：100ms 后结束，验证停机确实等待了排空
	f.inFlight.Store(2)
	time.AfterFunc(100*time.Millisecond, func() { f.inFlight.Store(0) })

	start := time.Now()
	if err := reg.StopAllWithDrain(2 * time.Second); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("should wait for in-flight tasks, returned in %v", elapsed)
	}
	if f.GetState() != StatePaused {
		t.Fatalf("daemon should be paused before stop, got %s", f.GetState())
	}
	if len(stopOrder) != 1 || stopOrder[0] != "parse" {
		t.Fatalf("stop hook should run once, got %v", stopOrder)
	}
}

func TestStopAllCapsDrainWait(t *testing.T) {
	reg := NewRegistry()
	f := newFake("parse")
	f.inFlight.Store(1) // 永不结束
	if err := reg.Register(Unit{Module: "parse", Handle: f, Stop: func() error { return nil }}); err != nil {
		t.Fatalf("register: %v", err)
	}

	start := time.Now()
	if err := reg.StopAllWithDrain(50 * time.Millisecond); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain wait must be capped, took %v", elapsed)
	}
}

func TestStopAllAggregatesErrors(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Unit{
		Module: "parse", Handle: newFake("parse"),
		Stop: func() error { return errors.New("close failed") },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	err := reg.StopAllWithDrain(0)
	if err == nil {
		t.Fatal("want aggregated error")
	}
}
