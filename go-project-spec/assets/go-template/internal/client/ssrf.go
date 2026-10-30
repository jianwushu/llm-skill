package client

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlockedTarget 目标地址被出站策略拒绝，调用方应映射为 400（而非 500）。
var ErrBlockedTarget = errors.New("target address is not allowed")

// SSRFPolicy 出站目标校验策略。
//
// 默认拒绝私网并不普适：工业内网/私有云里 10.x、192.168.x、127.0.0.1 上的服务
// 往往正是合法上游。因此策略可配，由"URL 是否用户可控"决定用哪一档：
//
//	用户可控 URL（前端传参）      → SSRFDenyPrivate
//	配置固定的可信内网上游        → SSRFAllowPrivate
//	半动态上游（域名/网段清单）   → SSRFAllowHosts + 白名单
type SSRFPolicy string

const (
	// SSRFDenyPrivate 拒绝回环/私网/链路本地地址，仅允许公网目标。
	SSRFDenyPrivate SSRFPolicy = "deny_private"
	// SSRFAllowPrivate 允许私网与回环（可信内网），仍拒绝链路本地与云元数据地址。
	SSRFAllowPrivate SSRFPolicy = "allow_private"
	// SSRFAllowHosts 只允许白名单内的主机名 / IP 前缀 / CIDR。
	SSRFAllowHosts SSRFPolicy = "allow_hosts"
)

// metadataCIDRs 云元数据地址，任何策略下都必须拒绝：一旦可达即可窃取实例凭证。
var metadataCIDRs = mustCIDRs(
	"169.254.169.254/32", // AWS / GCP / Azure / OpenStack
	"169.254.170.2/32",   // AWS ECS task metadata
	"100.100.100.200/32", // 阿里云
	"fd00:ec2::254/128",  // AWS IPv6
)

var metadataHosts = map[string]struct{}{
	"metadata":                 {},
	"metadata.google.internal": {},
}

func mustCIDRs(list ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, s := range list {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("client: bad metadata cidr " + s)
		}
		out = append(out, n)
	}
	return out
}

// Guard 出站目标校验器。三层生效：URL 入口校验、拨号 IP 校验（防 DNS Rebinding）、
// 重定向逐跳校验。
type Guard struct {
	policy SSRFPolicy
	allow  []string // 已归一化为小写
}

// NewGuard 创建校验器。policy 为空时取 SSRFDenyPrivate。
// allow 仅在 SSRFAllowHosts 下生效，支持三种写法：
//   - 精确主机名：api.partner.com
//   - 通配后缀：*.corp.example（含子域）
//   - IP 前缀 / CIDR：192.168.1. 或 10.0.0.0/8
func NewGuard(policy SSRFPolicy, allow []string) (*Guard, error) {
	switch policy {
	case "":
		policy = SSRFDenyPrivate
	case SSRFDenyPrivate, SSRFAllowPrivate, SSRFAllowHosts:
	default:
		return nil, fmt.Errorf("unknown ssrf policy %q (want deny_private|allow_private|allow_hosts)", policy)
	}

	normalized := make([]string, 0, len(allow))
	for _, h := range allow {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return nil, errors.New("ssrf allow list contains an empty entry")
		}
		normalized = append(normalized, h)
	}
	if policy == SSRFAllowHosts && len(normalized) == 0 {
		return nil, errors.New("ssrf policy allow_hosts requires a non-empty allow list")
	}
	return &Guard{policy: policy, allow: normalized}, nil
}

// Policy 返回当前策略，便于启动日志与配置回显。
func (g *Guard) Policy() SSRFPolicy { return g.policy }

// Check 校验 URL 入口：协议、主机名策略。
// 注意它不解析 DNS——域名解析后的 IP 校验在拨号阶段由 dialControl 完成。
func (g *Guard) Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http/https allowed, got %q", ErrBlockedTarget, u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: url host is empty", ErrBlockedTarget)
	}
	if _, ok := metadataHosts[host]; ok {
		return fmt.Errorf("%w: cloud metadata host %q", ErrBlockedTarget, host)
	}

	if ip := net.ParseIP(host); ip != nil {
		return g.checkIP(ip)
	}

	// 非 IP 字面量：allow_hosts 下按名字白名单校验；其余策略交给拨号期 IP 校验。
	if g.policy == SSRFAllowHosts && !g.hostAllowed(host) {
		return fmt.Errorf("%w: host %q is not in the allow list", ErrBlockedTarget, host)
	}
	return nil
}

// checkIP 按策略校验已解析的 IP。
func (g *Guard) checkIP(ip net.IP) error {
	if isMetadataIP(ip) {
		return fmt.Errorf("%w: cloud metadata address %s", ErrBlockedTarget, ip)
	}
	if g.policy == SSRFDenyPrivate {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("%w: private/loopback address %s", ErrBlockedTarget, ip)
		}
	}
	return nil
}

func (g *Guard) hostAllowed(host string) bool {
	for _, e := range g.allow {
		if e == host {
			return true
		}
		if strings.HasPrefix(e, "*.") && strings.HasSuffix(host, e[1:]) {
			return true
		}
		if strings.HasSuffix(e, ".") && strings.HasPrefix(host, e) {
			return true
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// dialControl 在 TCP 连接建立前拿到已解析地址，在这里校验 IP：
// 只有这样才能防住 DNS Rebinding（入口检查时域名解析结果与实际拨号不同）。
func (g *Guard) dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil // 非常规地址形态交给拨号本身报错
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	return g.checkIP(ip)
}

// checkRedirect 逐跳校验重定向目标，避免用一次 302 绕过入口检查。
func (g *Guard) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if err := g.Check(req.URL.String()); err != nil {
		return fmt.Errorf("redirect to %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

func isMetadataIP(ip net.IP) bool {
	for _, n := range metadataCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
