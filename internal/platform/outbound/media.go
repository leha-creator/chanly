package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var reservedMediaNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
}

func publicMediaIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	for _, network := range reservedMediaNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

// PublicMediaURL accepts exact hosts and explicit *.domain rules. A wildcard
// matches DNS subdomains only, never suffix lookalikes, credentials or ports.
func PublicMediaURL(raw string, hosts []string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return errors.New("untrusted media origin")
	}
	host, valid := mediaHostname(u.Hostname())
	if !valid {
		return errors.New("untrusted media origin")
	}
	for _, allowed := range hosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if strings.HasPrefix(allowed, "*.") {
			domain, valid := mediaHostname(strings.TrimPrefix(allowed, "*."))
			if valid && strings.Contains(domain, ".") && host != domain && strings.HasSuffix(host, "."+domain) {
				return nil
			}
		} else if exact, valid := mediaHostname(allowed); valid && host == exact {
			return nil
		}
	}
	return errors.New("untrusted media origin")
}

func mediaHostname(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return "", false
			}
		}
	}
	return value, true
}

// PublicMediaTransport pins each connection to a public DNS result and rejects redirects.
func PublicMediaClient(base *http.Client) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if base != nil {
		if b, ok := base.Transport.(*http.Transport); ok {
			t = b.Clone()
		}
	}
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, errors.New("invalid media address")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, errors.New("media DNS failed")
		}
		for _, ip := range ips {
			if !publicMediaIP(ip.IP) {
				return nil, errors.New("private media address rejected")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("media DNS empty")
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{Transport: t, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
