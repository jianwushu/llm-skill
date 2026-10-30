// Package client 统一封装对外部系统的客户端（HTTP / MQTT / MQ / Redis…）。
//
// 规范（唯一来源）：references/integration.md「HTTP：先判断要不要建客户端」
// 本文件即唯一实现，无副本——不要在其他位置另存一份再靠脚本校验同步。
//
// 约定：
//   - 只有本包可以直接构造外部客户端；service/engine 通过接口注入使用，禁止 new
//   - 超时必须显式设置，禁止使用零值 http.Client 等无超时默认值
//   - 重试只用于幂等操作 + 可重试错误（网络错误、5xx、429），指数退避 + 抖动
//   - 目标校验（SSRF）由 Guard 按策略执行，默认拒绝私网/回环
//   - 客户端在装配处创建，关闭顺序与创建顺序相反
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"time"
)

// DefaultMaxResponseBytes 响应体读取上限，防止上游返回超大内容打爆内存。
const DefaultMaxResponseBytes = 8 << 20 // 8MB

// Options 客户端参数。组件不依赖项目配置结构，由装配处从 config 映射过来，
// 这样才能跨项目复用同一份实现。
type Options struct {
	Timeout         time.Duration // 单次请求整体超时
	Retries         int           // 幂等请求的重试次数，0 表示不重试
	MaxIdleConns    int           // 全局空闲连接上限
	MaxIdlePerHost  int           // 单主机空闲连接上限（默认 2 太小）
	IdleConnTimeout time.Duration // 空闲连接回收时间
	MaxResponseSize int64         // 响应体读取上限，0 取 DefaultMaxResponseBytes
	Guard           *Guard        // 出站目标校验策略；nil 表示不校验
}

// HTTPClient 带超时、重试与出站目标校验的 JSON HTTP 客户端。
type HTTPClient struct {
	hc       *http.Client
	retries  int
	baseWait time.Duration
	maxBody  int64
	guard    *Guard
}

// New 创建客户端。所有参数有安全默认值，但 Timeout 强烈建议显式配置。
func New(opt Options) *HTTPClient {
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.Retries < 0 {
		opt.Retries = 0
	}
	if opt.MaxIdleConns <= 0 {
		opt.MaxIdleConns = 100
	}
	if opt.MaxIdlePerHost <= 0 {
		opt.MaxIdlePerHost = 20
	}
	if opt.IdleConnTimeout <= 0 {
		opt.IdleConnTimeout = 90 * time.Second
	}
	if opt.MaxResponseSize <= 0 {
		opt.MaxResponseSize = DefaultMaxResponseBytes
	}

	// 拨号超时与整体超时分开设置：整体超时管不住「连不上就一直等」之外的中间态。
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if opt.Guard != nil {
		// 在解析出 IP 后校验，防止 DNS Rebinding（域名先解析到公网、再解析到内网）。
		dialer.Control = opt.Guard.dialControl
	}

	hc := &http.Client{
		Timeout: opt.Timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConns:        opt.MaxIdleConns,
			MaxIdleConnsPerHost: opt.MaxIdlePerHost,
			IdleConnTimeout:     opt.IdleConnTimeout,
		},
	}
	if opt.Guard != nil {
		// 重定向同样要校验：否则一个 302 就能绕过入口检查。
		hc.CheckRedirect = opt.Guard.checkRedirect
	}

	return &HTTPClient{
		hc:       hc,
		retries:  opt.Retries,
		baseWait: 200 * time.Millisecond,
		maxBody:  opt.MaxResponseSize,
		guard:    opt.Guard,
	}
}

// GetJSON GET 并反序列化响应体到 out。GET 幂等，失败按指数退避重试。
func (c *HTTPClient) GetJSON(ctx context.Context, url string, out any) error {
	return c.DoJSON(ctx, http.MethodGet, url, nil, out)
}

// DoJSON 以指定方法请求并反序列化响应。
//
// 重试策略按方法判定：只有幂等方法（GET/HEAD/PUT/DELETE/OPTIONS）会重试；
// POST/PATCH 默认不重试——除非上游支持幂等键，否则重试会造成重复写入。
// 需要 POST 重试时，把幂等键放进请求头或 body，并在此处显式放开。
func (c *HTTPClient) DoJSON(ctx context.Context, method, url string, body []byte, out any) error {
	if c.guard != nil {
		if err := c.guard.Check(url); err != nil {
			return fmt.Errorf("target rejected: %w", err)
		}
	}
	attempts := 1
	if isIdempotent(method) {
		attempts = c.retries + 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			wait := c.baseWait << (attempt - 1)
			jitter := time.Duration(rand.Int63n(int64(wait/2) + 1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait + jitter):
			}
		}

		err := c.doOnce(ctx, method, url, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable(err) || attempt == attempts-1 {
			break
		}
	}
	if attempts > 1 {
		return fmt.Errorf("request failed after %d attempts: %w", attempts, lastErr)
	}
	return lastErr
}

func (c *HTTPClient) doOnce(ctx context.Context, method, url string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err // 网络层错误，交由 retryable 判定
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &httpStatusError{Code: resp.StatusCode, Body: string(snippet)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, c.maxBody)).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // 空响应体：视为无数据，不当作错误
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// CloseIdleConnections 进程退出前调用（关闭顺序与创建顺序相反）。
func (c *HTTPClient) CloseIdleConnections() {
	c.hc.CloseIdleConnections()
}

// IsIdempotent 暴露给调用方判断某方法是否可安全重试。
func IsIdempotent(method string) bool { return isIdempotent(method) }

func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

type httpStatusError struct {
	Code int
	Body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d: %s", e.Code, e.Body)
}

// StatusCode 便于调用方分类处理（如 401 跳登录）。
func (e *httpStatusError) StatusCode() int { return e.Code }

// retryable 仅网络错误与 5xx/429 可重试；4xx 属调用方错误，立即返回。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.Code >= 500 || se.Code == http.StatusTooManyRequests
	}
	// 上下文取消/超时不重试，否则会白等到超时上限
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true // 网络层错误
}
