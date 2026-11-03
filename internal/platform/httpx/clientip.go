package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/leha-creator/chanly/internal/platform/audit"
)

type clientKey struct{}

type clientInfo struct {
	ip        *netip.Addr
	userAgent string
}

// ClientInfo определяет IP клиента и User-Agent. IP берётся из RemoteAddr;
// X-Forwarded-For учитывается, только если запрос пришёл от доверенного прокси
// (TRUSTED_PROXY_CIDRS): тогда берётся крайний справа адрес вне доверенных сетей.
func ClientInfo(trusted []netip.Prefix) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := clientInfo{ip: clientIP(r, trusted), userAgent: r.UserAgent()}
			ctx := context.WithValue(r.Context(), clientKey{}, info)
			actor := audit.ActorFrom(ctx)
			actor.IP = info.ip
			ctx = audit.WithActor(ctx, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP возвращает IP клиента; nil, если он неизвестен.
func ClientIP(ctx context.Context) *netip.Addr {
	info, _ := ctx.Value(clientKey{}).(clientInfo)
	return info.ip
}

// UserAgent возвращает заголовок User-Agent запроса.
func UserAgent(ctx context.Context) string {
	info, _ := ctx.Value(clientKey{}).(clientInfo)
	return info.userAgent
}

// ParsePrefixes разбирает список CIDR.
func ParsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func clientIP(r *http.Request, trusted []netip.Prefix) *netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	remote = remote.Unmap()
	if !inPrefixes(remote, trusted) {
		return &remote
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !inPrefixes(a, trusted) {
			return &a
		}
	}
	return &remote
}

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
