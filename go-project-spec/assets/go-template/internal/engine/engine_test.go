package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"testing"
	"time"
)

type funcEngine struct {
	name string
	fn   func(ctx context.Context, payload []byte) (any, error)
}

func (e funcEngine) Name() string        { return e.name }
func (e funcEngine) Description() string { return "test engine" }
func (e funcEngine) Execute(ctx context.Context, payload []byte) (any, error) {
	return e.fn(ctx, payload)
}

// mustRegister 测试用：注册失败直接终止用例。
func mustRegister(t *testing.T, d *Dispatcher, engines ...Engine) {
	t.Helper()
	if err := d.Register(engines...); err != nil {
		t.Fatalf("register engines: %v", err)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	d := NewDispatcher(1, time.Second)
	mustRegister(t, d, TextStatsEngine{})

	// 与已注册同名 → 报错，且不覆盖原实现
	if err := d.Register(TextStatsEngine{}); !errors.Is(err, ErrDuplicateEngine) {
		t.Fatalf("want ErrDuplicateEngine, got %v", err)
	}
	// 同批内重名 → 报错
	if err := d.Register(funcEngine{name: "dup", fn: func(context.Context, []byte) (any, error) { return nil, nil }},
		funcEngine{name: "dup", fn: func(context.Context, []byte) (any, error) { return nil, nil }}); !errors.Is(err, ErrDuplicateEngine) {
		t.Fatalf("want ErrDuplicateEngine for in-batch duplicate, got %v", err)
	}
	// 空名称 / nil → ErrInvalidEngine
	if err := d.Register(funcEngine{name: "", fn: func(context.Context, []byte) (any, error) { return nil, nil }}); !errors.Is(err, ErrInvalidEngine) {
		t.Fatalf("want ErrInvalidEngine for empty name, got %v", err)
	}
	if err := d.Register(nil); !errors.Is(err, ErrInvalidEngine) {
		t.Fatalf("want ErrInvalidEngine for nil, got %v", err)
	}
	// 失败批次不得留下部分注册
	if d.Len() != 1 {
		t.Fatalf("failed register must not partially commit, len=%d", d.Len())
	}
}

func TestReplace(t *testing.T) {
	d := NewDispatcher(1, time.Second)
	mustRegister(t, d, funcEngine{name: "x", fn: func(context.Context, []byte) (any, error) { return "old", nil }})
	if err := d.Replace(funcEngine{name: "x", fn: func(context.Context, []byte) (any, error) { return "new", nil }}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	res, err := d.Run(context.Background(), Request{Engine: "x"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Output != "new" {
		t.Fatalf("replace not applied: %v", res.Output)
	}
	if d.Len() != 1 {
		t.Fatalf("replace must not add entries, len=%d", d.Len())
	}
}

func TestRunOK(t *testing.T) {
	d := NewDispatcher(2, time.Second)
	mustRegister(t, d, TextStatsEngine{})

	res, err := d.Run(context.Background(), Request{
		Engine:  "text-stats",
		Payload: []byte(`{"text":"hello world\n第二行","keyword":"hello"}`),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	out, ok := res.Output.(textStatsOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", res.Output)
	}
	if out.Words != 3 || out.Lines != 2 || out.KeywordHits != 1 {
		t.Fatalf("unexpected stats: %+v", out)
	}
	if len(d.List()) != 1 || d.List()[0].Name != "text-stats" {
		t.Fatalf("unexpected engine list: %+v", d.List())
	}
}

func TestErrNotFound(t *testing.T) {
	d := NewDispatcher(1, time.Second)
	if _, err := d.Run(context.Background(), Request{Engine: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestBusy(t *testing.T) {
	d := NewDispatcher(1, 2*time.Second)
	release := make(chan struct{})
	mustRegister(t, d, funcEngine{name: "slow", fn: func(ctx context.Context, _ []byte) (any, error) {
		<-release
		return nil, nil
	}})

	go func() { _, _ = d.Run(context.Background(), Request{Engine: "slow"}) }()
	time.Sleep(50 * time.Millisecond) // 等第一个请求占满并发令牌

	if _, err := d.Run(context.Background(), Request{Engine: "slow"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	close(release)
}

func TestTimeout(t *testing.T) {
	d := NewDispatcher(1, 50*time.Millisecond)
	mustRegister(t, d, funcEngine{name: "hang", fn: func(ctx context.Context, _ []byte) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})

	_, err := d.Run(context.Background(), Request{Engine: "hang"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestPanicIsolation(t *testing.T) {
	d := NewDispatcher(1, time.Second)
	mustRegister(t, d, funcEngine{name: "boom", fn: func(ctx context.Context, _ []byte) (any, error) {
		panic("engine bug")
	}})

	if _, err := d.Run(context.Background(), Request{Engine: "boom"}); err == nil {
		t.Fatal("want error from panic, got nil")
	}
	// 进程仍可用，后续请求正常
	mustRegister(t, d, funcEngine{name: "ok", fn: func(ctx context.Context, _ []byte) (any, error) { return "fine", nil }})
	if _, err := d.Run(context.Background(), Request{Engine: "ok"}); err != nil {
		t.Fatalf("dispatcher should survive panic: %v", err)
	}
}

func TestImageCompress(t *testing.T) {
	var src bytes.Buffer
	if err := png.Encode(&src, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(imageCompressPayload{
		DataBase64: base64.StdEncoding.EncodeToString(src.Bytes()),
		Quality:    60,
	})
	if err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(1, 2*time.Second)
	mustRegister(t, d, ImageCompressEngine{})
	res, err := d.Run(context.Background(), Request{Engine: "image-compress", Payload: payload})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	out, ok := res.Output.(imageCompressOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", res.Output)
	}
	if out.Width != 40 || out.Height != 30 {
		t.Fatalf("unexpected size: %dx%d", out.Width, out.Height)
	}
	if out.BeforeSize == 0 || out.AfterSize == 0 {
		t.Fatalf("unexpected byte sizes: %+v", out)
	}
}

func TestInvalidInputMapped(t *testing.T) {
	d := NewDispatcher(1, time.Second)
	mustRegister(t, d, TextStatsEngine{})

	_, err := d.Run(context.Background(), Request{Engine: "text-stats", Payload: []byte(`{"text":""}`)})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}
