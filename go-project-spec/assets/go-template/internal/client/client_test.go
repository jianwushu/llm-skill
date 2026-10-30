package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoJSONRetriesIdempotent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(Options{Timeout: 2 * time.Second, Retries: 3, Guard: testGuard(t, SSRFAllowPrivate, nil)})
	var out map[string]bool
	if err := c.GetJSON(context.Background(), srv.URL, &out); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !out["ok"] {
		t.Fatalf("unexpected body: %+v", out)
	}
	if hits != 3 {
		t.Fatalf("want 3 attempts, got %d", hits)
	}
}

func TestDoJSONNoRetryOn4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`denied`))
	}))
	defer srv.Close()

	c := New(Options{Timeout: 2 * time.Second, Retries: 3, Guard: testGuard(t, SSRFAllowPrivate, nil)})
	err := c.GetJSON(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("want error for 400")
	}
	var se *httpStatusError
	if !errors.As(err, &se) || se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("want httpStatusError 400, got %v", err)
	}
	if hits != 1 {
		t.Fatalf("4xx must not retry, got %d attempts", hits)
	}
}

func TestDoJSONPostNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Options{Timeout: 2 * time.Second, Retries: 3, Guard: testGuard(t, SSRFAllowPrivate, nil)})
	if err := c.DoJSON(context.Background(), http.MethodPost, srv.URL, []byte(`{}`), nil); err == nil {
		t.Fatal("want error for 500")
	}
	if hits != 1 {
		t.Fatalf("POST must not be retried by default, got %d attempts", hits)
	}
}

func TestGuardBlocksPrivateByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close() // 监听 127.0.0.1

	c := New(Options{Timeout: time.Second, Retries: 0, Guard: testGuard(t, SSRFDenyPrivate, nil)})
	err := c.GetJSON(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("want ErrBlockedTarget, got %v", err)
	}
}

func TestGuardPolicyMatrix(t *testing.T) {
	cases := []struct {
		name    string
		policy  SSRFPolicy
		allow   []string
		url     string
		blocked bool
	}{
		{"deny_private 拒绝回环", SSRFDenyPrivate, nil, "http://127.0.0.1:8080/x", true},
		{"deny_private 拒绝私网", SSRFDenyPrivate, nil, "http://10.1.2.3/x", true},
		{"deny_private 放行公网", SSRFDenyPrivate, nil, "http://93.184.216.34/x", false},
		{"deny_private 拒绝元数据", SSRFDenyPrivate, nil, "http://169.254.169.254/latest/meta-data", true},
		{"allow_private 放行回环", SSRFAllowPrivate, nil, "http://127.0.0.1:8080/x", false},
		{"allow_private 放行私网", SSRFAllowPrivate, nil, "http://192.168.1.100:9000/api", false},
		{"allow_private 仍拒元数据", SSRFAllowPrivate, nil, "http://169.254.169.254/latest", true},
		{"allow_private 拒元数据域名", SSRFAllowPrivate, nil, "http://metadata.google.internal/x", true},
		{"拒绝非 http 协议", SSRFAllowPrivate, nil, "file:///etc/passwd", true},
		{"白名单命中通配后缀", SSRFAllowHosts, []string{"*.corp.example"}, "http://api.corp.example/x", false},
		{"白名单未命中", SSRFAllowHosts, []string{"*.corp.example"}, "http://evil.com/x", true},
		{"白名单命中 IP 前缀", SSRFAllowHosts, []string{"10.10."}, "http://10.10.1.20:8080/x", false},
		{"白名单命中 CIDR", SSRFAllowHosts, []string{"192.168.0.0/16"}, "http://192.168.5.6/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewGuard(tc.policy, tc.allow)
			if err != nil {
				t.Fatalf("new guard: %v", err)
			}
			err = g.Check(tc.url)
			if tc.blocked && !errors.Is(err, ErrBlockedTarget) {
				t.Fatalf("want blocked, got %v", err)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("want allowed, got %v", err)
			}
		})
	}
}

func TestNewGuardValidation(t *testing.T) {
	if _, err := NewGuard("nope", nil); err == nil {
		t.Fatal("want error for unknown policy")
	}
	if _, err := NewGuard(SSRFAllowHosts, nil); err == nil {
		t.Fatal("want error for allow_hosts without list")
	}
	if _, err := NewGuard(SSRFAllowHosts, []string{"  "}); err == nil {
		t.Fatal("want error for empty allow entry")
	}
}

// testGuard 测试辅助：策略固定、忽略错误。
func testGuard(t *testing.T, p SSRFPolicy, allow []string) *Guard {
	t.Helper()
	g, err := NewGuard(p, allow)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}
