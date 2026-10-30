package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// upperStep 示例算子：把 Body 转大写，演示纯函数式实现。
type upperStep struct{}

func (upperStep) Type() string { return "upper" }

func (upperStep) Validate(cfg json.RawMessage) error {
	var c struct {
		Suffix string `json:"suffix"`
	}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &c); err != nil {
			return err
		}
	}
	if c.Suffix == "" {
		return nil
	}
	return nil
}

func (upperStep) Apply(_ context.Context, in *Record, _ json.RawMessage) (*Record, error) {
	out := *in
	out.Body = []byte(strings.ToUpper(string(in.Body)))
	return &out, nil
}

// badCfgStep 永远校验失败，用于验证 ValidateAll 前置拦截。
type badCfgStep struct{}

func (badCfgStep) Type() string                       { return "bad" }
func (badCfgStep) Validate(cfg json.RawMessage) error { return errors.New("cfg invalid") }
func (badCfgStep) Apply(context.Context, *Record, json.RawMessage) (*Record, error) {
	return &Record{}, nil
}

// nilStep 返回 nil，验证编排器拦截。
type nilStep struct{}

func (nilStep) Type() string                   { return "nil" }
func (nilStep) Validate(json.RawMessage) error { return nil }
func (nilStep) Apply(context.Context, *Record, json.RawMessage) (*Record, error) {
	return nil, nil
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(upperStep{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(upperStep{}); !errors.Is(err, ErrDuplicateStep) {
		t.Fatalf("want ErrDuplicateStep, got %v", err)
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("nil step should be rejected")
	}
}

func TestRunAppliesStepsInOrder(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(upperStep{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	in := &Record{Path: "a.txt", Body: []byte("hello")}
	out, err := r.Run(context.Background(), []StepDef{{Type: "upper"}, {Type: "upper"}}, in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out.Body) != "HELLO" {
		t.Fatalf("want HELLO, got %s", out.Body)
	}
	if string(in.Body) != "hello" {
		t.Fatal("input record must not be mutated")
	}
}

func TestRunUnknownStep(t *testing.T) {
	r := NewRegistry()
	_, err := r.Run(context.Background(), []StepDef{{Type: "nope"}}, &Record{})
	if !errors.Is(err, ErrUnknownStep) {
		t.Fatalf("want ErrUnknownStep, got %v", err)
	}
}

func TestValidateAllRejectsBadConfig(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(badCfgStep{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	err := r.ValidateAll([]StepDef{{Type: "bad"}})
	if err == nil || !strings.Contains(err.Error(), "cfg invalid") {
		t.Fatalf("want config error, got %v", err)
	}
}

func TestRunRejectsNilRecordAndNilResult(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nilStep{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := r.Run(context.Background(), nil, nil); err == nil {
		t.Fatal("nil input should be rejected")
	}
	if _, err := r.Run(context.Background(), []StepDef{{Type: "nil"}}, &Record{}); err == nil {
		t.Fatal("nil result from step should be rejected")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(upperStep{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Run(ctx, []StepDef{{Type: "upper"}}, &Record{Body: []byte("x")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestTypesSorted(t *testing.T) {
	r := NewRegistry()
	for _, s := range []Step{upperStep{}, nilStep{}, badCfgStep{}} {
		if err := r.Register(s); err != nil {
			t.Fatalf("register %s: %v", s.Type(), err)
		}
	}
	got := r.Types()
	want := []string{"bad", "nil", "upper"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("types not sorted: %v", got)
		}
	}
}
