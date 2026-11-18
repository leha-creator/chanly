package outbound

import (
	"net"
	"testing"
)

func TestPublicMediaURL(t *testing.T) {
	for _, u := range []string{"http://cdn.example.test/x", "https://cdn.example.test.evil/x", "https://user:password@cdn.example.test/x", "https://cdn.example.test:443/x", "https://127.0.0.1/x"} {
		if PublicMediaURL(u, []string{"cdn.example.test"}) == nil {
			t.Fatalf("accepted %q", u)
		}
	}
	if PublicMediaURL("https://cdn.example.test/x?signature=secret", []string{"cdn.example.test"}) != nil {
		t.Fatal("valid CDN rejected")
	}
}

func TestPublicMediaIP(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "192.0.2.1", "198.18.0.1", "240.1.2.3", "::1", "fc00::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::a00:1"} {
		if publicMediaIP(net.ParseIP(address)) {
			t.Fatalf("non-public media IP accepted: %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicMediaIP(net.ParseIP(address)) {
			t.Fatalf("public media IP rejected: %s", address)
		}
	}
}

func TestPublicMediaDomainFamilies(t *testing.T) {
	rules := []string{"oneme.ru", "*.oneme.ru", "okcdn.ru", "*.okcdn.ru"}
	for _, host := range []string{"oneme.ru", "i.oneme.ru", "iu.oneme.ru", "fd.oneme.ru", "cdn-17.region.oneme.ru", "okcdn.ru", "omu.okcdn.ru", "new.region.okcdn.ru", "I.ONEME.RU", "i.oneme.ru."} {
		if PublicMediaURL("https://"+host+"/private?signature=secret", rules) != nil {
			t.Errorf("trusted CDN host rejected: %s", host)
		}
	}
	for _, raw := range []string{"https://eviloneme.ru/x", "https://oneme.ru.evil.test/x", "https://i.oneme.ru.evil.test/x", "https://evilokcdn.ru/x", "https://okcdn.ru.evil.test/x", "https://oneme.ru@evil.test/x", "https://evil.test@i.oneme.ru/x", "https://i.oneme.ru:443/x", "http://i.oneme.ru/x", "https://i..oneme.ru/x", "https://i.oneme.ru../x", "https://127.0.0.1/x", "https://[::1]/x"} {
		if PublicMediaURL(raw, rules) == nil {
			t.Errorf("untrusted CDN origin accepted: %s", raw)
		}
	}
	if PublicMediaURL("https://child.cdn.example.test/x", []string{"cdn.example.test"}) == nil {
		t.Fatal("exact host silently became a domain wildcard")
	}
	if PublicMediaURL("https://oneme.ru/x", []string{"*.oneme.ru"}) == nil {
		t.Fatal("wildcard unexpectedly matched the parent domain")
	}
	if PublicMediaURL("https://untrusted.ru/x", []string{"*.ru", "*"}) == nil {
		t.Fatal("top-level wildcard accepted")
	}
}
