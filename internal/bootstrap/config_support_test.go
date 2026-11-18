package bootstrap

import (
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/outbound"
)

func TestSupportConfigurationIsolation(t *testing.T) {
	c := Config{}
	var fields []string
	add := func(k, v string) { fields = append(fields, k) }
	c.validateSupport(add)
	if len(fields) != 0 {
		t.Fatal("disabled support requires settings")
	}
	c.MAX.BotToken = "same"
	c.SupportMAX = SupportMAXConfig{Enabled: true, BotToken: "same", BotUserID: 2, WebhookURL: "https://example.test/api/v1/webhooks/max-support", WebhookSecret: strings.Repeat("s", 32), WebhookPathSecret: strings.Repeat("p", 32)}
	c.validateSupport(add)
	found := false
	for _, f := range fields {
		if f == "SUPPORT_MAX_BOT_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatal("same bot allowed")
	}
}

func TestDefaultSupportCDNDomains(t *testing.T) {
	c := testConfig()
	for _, host := range []string{"i.oneme.ru", "fd.oneme.ru", "future.cdn.oneme.ru", "new.cdn.okcdn.ru"} {
		if outbound.PublicMediaURL("https://"+host+"/media?signature=secret", c.SupportMAX.MediaHosts) != nil {
			t.Fatalf("default support configuration blocks CDN: %s", host)
		}
	}
	if outbound.PublicMediaURL("https://i.oneme.ru.evil.test/media", c.SupportMAX.MediaHosts) == nil {
		t.Fatal("default support configuration accepts lookalike domain")
	}
}
