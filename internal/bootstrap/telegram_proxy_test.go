package bootstrap

import (
	"net/http"
	"net/url"
	"testing"
)

func TestTelegramProxyTransportIsIsolated(t *testing.T) {
	original := http.DefaultTransport
	client := telegramHTTPClient("http://telegram-egress:18081")
	transport := client.Transport.(*http.Transport)
	u, _ := url.Parse("https://api.telegram.org/bot123:test/getMe")
	proxy, err := transport.Proxy(&http.Request{URL: u})
	if err != nil || proxy.String() != "http://telegram-egress:18081" {
		t.Fatal("Telegram did not select the configured proxy")
	}
	if transport == original || http.DefaultTransport != original {
		t.Fatal("Telegram changed the shared HTTP transport")
	}
}

func TestTelegramProxyConfigurationRejectsCredentialsAndPaths(t *testing.T) {
	for _, value := range []string{"http://telegram-egress:18081", "socks5://proxy:1080", "http://", "http://user:pass@proxy", "http://proxy/path", "http://proxy?q=x"} {
		cfg := Config{Telegram: TelegramConfig{ProxyURL: value}}
		errors := 0
		cfg.validateTelegram(func(string, string) { errors++ })
		if (errors == 0) != (value == "http://telegram-egress:18081") {
			t.Fatalf("unexpected validation result for proxy %q", value)
		}
	}
}
