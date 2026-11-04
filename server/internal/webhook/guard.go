package webhook

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Причины отказа — как строки WebhookUrlGuard::check() (идут в last_error как "blocked: <причина>").
const (
	ReasonInvalidURL     = "invalid_url"
	ReasonHTTPSRequired  = "https_required"
	ReasonPortNotAllowed = "port_not_allowed"
	ReasonNotResolved    = "host_not_resolved"
	ReasonPrivateAddress = "private_address"
)

// DefaultPorts — разрешённые порты (WebhookUrlGuard::check(): [80, 443, 8080, 8443]).
var DefaultPorts = []int{80, 443, 8080, 8443}

// Target — проверенная цель: запрос «пришпиливается» к IP, чтобы DNS не подменили между проверкой и
// соединением (DNS rebinding) — CURLOPT_RESOLVE в старом коде.
type Target struct {
	Host string
	Port int
	IP   netip.Addr
}

// Guard — защита от SSRF для URL вебхуков.
// Laravel: app/Services/Integrations/WebhookUrlGuard.php WebhookUrlGuard::check().
type Guard struct {
	// RequireHTTPS — integrations.webhook_require_https (WEBHOOK_REQUIRE_HTTPS, по умолчанию true).
	RequireHTTPS bool
	// AllowPrivate — integrations.webhook_allow_private_hosts (WEBHOOK_ALLOW_PRIVATE_HOSTS, по умолчанию false).
	AllowPrivate bool
	// Ports — разрешённые порты (nil — DefaultPorts; пустой не-nil срез — любые, только для тестов).
	Ports []int
	// Resolve — DNS (nil — net.DefaultResolver).
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Check проверяет URL и возвращает цель или причину отказа (reason != "").
func (g Guard) Check(ctx context.Context, raw string) (Target, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, ReasonInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if (scheme != "http" && scheme != "https") || host == "" || u.User != nil {
		return Target{}, ReasonInvalidURL
	}
	if scheme != "https" && g.RequireHTTPS {
		return Target{}, ReasonHTTPSRequired
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Target{}, ReasonInvalidURL
		}
		port = n
	}
	ports := g.Ports
	if ports == nil {
		ports = DefaultPorts
	}
	if len(ports) > 0 && !slices.Contains(ports, port) {
		return Target{}, ReasonPortNotAllowed
	}

	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		resolve := g.Resolve
		if resolve == nil {
			resolve = defaultResolve
		}
		ips, _ = resolve(ctx, host)
	}
	if len(ips) == 0 {
		return Target{}, ReasonNotResolved
	}
	if !g.AllowPrivate {
		for _, ip := range ips {
			if !IsPublic(ip) {
				return Target{}, ReasonPrivateAddress
			}
		}
	}
	return Target{Host: host, Port: port, IP: ips[0].Unmap()}, ""
}

// defaultResolve — A, затем AAAA (как dns_get_record(DNS_A | DNS_AAAA): IPv4 первым).
func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(addrs, func(a, b netip.Addr) int {
		switch {
		case a.Unmap().Is4() && !b.Unmap().Is4():
			return -1
		case !a.Unmap().Is4() && b.Unmap().Is4():
			return 1
		}
		return 0
	})
	return addrs, nil
}

// Диапазоны, которые PHP FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE отвергает, плюс несколько
// служебных сетей, которых в PHP-фильтре нет (CGNAT, документационные, 6to4/NAT64-префиксы на приватные
// адреса не раскрываются) — строже старого, наружу вебхуки из таких сетей не нужны.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// IsPublic — адрес из публичного интернета (не частный, не loopback, не link-local, не служебный).
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
