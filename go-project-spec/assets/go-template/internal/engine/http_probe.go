package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// jsonGetter 抽象外部 HTTP JSON 能力（由 internal/client 实现）。
// 引擎只依赖接口，便于测试替身，也避免引擎直接持有具体客户端。
type jsonGetter interface {
	GetJSON(ctx context.Context, url string, out any) error
}

// urlGuard 出站目标校验（由 internal/client.Guard 实现）。
// 引擎只声明"需要校验"，具体策略（拒绝私网 / 允许内网 / 白名单）由装配处决定。
type urlGuard interface {
	Check(raw string) error
}

// HTTPProbeEngine 调用外部 JSON 接口的引擎示例，演示"引擎 + 外部依赖注入"。
// 生产中的外部调用（第三方 API、内部服务）都按此模式：客户端注入引擎，引擎只描述业务语义。
type HTTPProbeEngine struct {
	upstream jsonGetter
	guard    urlGuard
}

func NewHTTPProbeEngine(upstream jsonGetter, guard urlGuard) HTTPProbeEngine {
	return HTTPProbeEngine{upstream: upstream, guard: guard}
}

func (HTTPProbeEngine) Name() string { return "http-probe" }

func (HTTPProbeEngine) Description() string {
	return "请求外部 JSON 接口，返回顶层字段名清单（演示外部 HTTP 依赖注入）"
}

type httpProbePayload struct {
	URL string `json:"url"`
}

type httpProbeOutput struct {
	Fields     []string `json:"fields"`
	FieldCount int      `json:"field_count"`
}

func (e HTTPProbeEngine) Execute(ctx context.Context, payload []byte) (any, error) {
	var p httpProbePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("%w: malformed payload: %v", ErrInvalidInput, err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("%w: payload.url is required", ErrInvalidInput)
	}
	if e.guard != nil {
		if err := e.guard.Check(p.URL); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}

	out := map[string]any{}
	if err := e.upstream.GetJSON(ctx, p.URL, &out); err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}

	fields := make([]string, 0, len(out))
	for k := range out {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return httpProbeOutput{Fields: fields, FieldCount: len(fields)}, nil
}
