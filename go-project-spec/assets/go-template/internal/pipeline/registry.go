// Package pipeline 是算子（步骤）注册表与编排器。
//
// 目标：消除「新增一个算子要同步改校验 switch + 执行 switch + 格式分发 + 前端表单」的联动维护。
// 新增算子 = 新增一个文件 + 一处 Register，编排器（Registry.Run）与路由层均不需改动。
//
// 约束（详见 references/tasks.md「算子注册表」）：
//   - 算子必须是纯函数式：只读写 *Record，不写库、不发网络、不读全局配置；
//   - 算子配置通过 cfg json.RawMessage 传入，由 Step.Validate 校验；
//   - 需要落库/发请求的副作用放在链路层（daemon），不放算子层。
//
// Record 归属本包：daemon 只通过 pipeline.StepDef / pipeline.Record 使用编排能力，
// 不反向在 daemon 里另立一套记录定义（否则两套字段会逐渐漂移）。
//
// 可变性契约（*Record、map、[]byte 都是引用类型，必须显式约定归属）：
//   - 编排器保证同一时刻只有一步持有当前记录；算子可以原地修改并返回同一个 *Record，
//     也可以返回新记录 —— 两种都合法；
//   - 算子不得把 *Record 或其字段（Meta/Fields/Body）保存到包级变量、缓存、或异步 goroutine；
//   - 需要并行跑多个分支、或需要保留输入原样时，必须先 Clone 再交给分支。
//
// 若项目没有流水线需求，删除本包及装配处引用即可。
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Record 在算子链中流转的数据载体。
type Record struct {
	Path   string            `json:"path"`             // 源文件/记录标识
	Body   []byte            `json:"-"`                // 内容（大对象建议只存路径，不常驻内存）
	Meta   map[string]string `json:"meta,omitempty"`   // 提取的元数据，算子可读写
	Fields map[string]any    `json:"fields,omitempty"` // 结构化输出字段
}

// Clone 深拷贝记录（Body 与两个 map 都复制）。
// 并行分支、或需要保留输入原样对照时用它，避免多个分支共享同一底层数组互相污染。
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	out := &Record{Path: r.Path}
	if r.Body != nil {
		out.Body = make([]byte, len(r.Body))
		copy(out.Body, r.Body)
	}
	if r.Meta != nil {
		out.Meta = make(map[string]string, len(r.Meta))
		for k, v := range r.Meta {
			out.Meta[k] = v
		}
	}
	if r.Fields != nil {
		out.Fields = make(map[string]any, len(r.Fields))
		for k, v := range r.Fields {
			out.Fields[k] = v // 浅拷贝值：Fields 内的复合值约定为不可变
		}
	}
	return out
}

// StepDef 一条步骤定义（来自配置/请求体），Type 对应 Step.Type()。
type StepDef struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Step 算子契约。实现必须纯函数式：输入 Record → 输出 Record。
type Step interface {
	Type() string                       // 唯一标识：extract-text / ocr / resize...
	Validate(cfg json.RawMessage) error // 校验该算子的配置，供保存与预检复用
	Apply(ctx context.Context, in *Record, cfg json.RawMessage) (*Record, error)
}

var (
	// ErrUnknownStep 未注册的算子类型。
	ErrUnknownStep = errors.New("unknown step type")
	// ErrDuplicateStep 重复注册同一 Type。
	ErrDuplicateStep = errors.New("duplicate step type")
)

// Registry 算子注册表，并发安全。
type Registry struct {
	mu    sync.RWMutex
	steps map[string]Step
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{steps: make(map[string]Step)}
}

// Register 注册算子；Type 为空或重复时报错（重复注册通常是复制粘贴漏改 Type）。
func (r *Registry) Register(s Step) error {
	if s == nil {
		return errors.New("pipeline: step is nil")
	}
	name := s.Type()
	if name == "" {
		return errors.New("pipeline: step type is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.steps[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateStep, name)
	}
	r.steps[name] = s
	return nil
}

// Types 返回已注册算子类型（排序后，供能力发现接口使用）。
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.steps))
	for name := range r.steps {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Lookup 取算子。
func (r *Registry) Lookup(name string) (Step, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.steps[name]
	return s, ok
}

// ValidateAll 逐步骤校验：算子存在 + 配置合法。
// 在保存流程定义或批次开始前调用，避免跑到一半才发现配置错误。
func (r *Registry) ValidateAll(defs []StepDef) error {
	for i, def := range defs {
		s, ok := r.Lookup(def.Type)
		if !ok {
			return fmt.Errorf("step[%d]: %w: %q", i, ErrUnknownStep, def.Type)
		}
		if err := s.Validate(def.Config); err != nil {
			return fmt.Errorf("step[%d] (%s): %w", i, def.Type, err)
		}
	}
	return nil
}

// Run 按顺序执行步骤链。任一环节出错即终止并带上步骤上下文。
// 注意：编排逻辑固定，新增算子不得修改本函数。
func (r *Registry) Run(ctx context.Context, defs []StepDef, in *Record) (*Record, error) {
	if in == nil {
		return nil, errors.New("pipeline: input record is nil")
	}
	if err := r.ValidateAll(defs); err != nil {
		return nil, err
	}

	cur := in
	for i, def := range defs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("step[%d] (%s): %w", i, def.Type, err)
		}
		s, _ := r.Lookup(def.Type) // ValidateAll 已保证存在
		out, err := s.Apply(ctx, cur, def.Config)
		if err != nil {
			return nil, fmt.Errorf("step[%d] (%s): %w", i, def.Type, err)
		}
		if out == nil {
			return nil, fmt.Errorf("step[%d] (%s): returned nil record", i, def.Type)
		}
		cur = out
	}
	return cur, nil
}
