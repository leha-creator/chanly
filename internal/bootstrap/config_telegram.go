package bootstrap

import (
	"net/url"
	"regexp"
	"strings"
)

type TelegramConfig struct {
	Enabled       bool   `env:"TELEGRAM_ENABLED" envDefault:"false"`
	DeliveryMode  string `env:"TELEGRAM_DELIVERY_MODE" envDefault:"webhook"`
	BotName       string `env:"TELEGRAM_BOT_NAME"`
	BotToken      string `env:"TELEGRAM_BOT_TOKEN"`
	WebhookSecret string `env:"TELEGRAM_WEBHOOK_SECRET"`
	WebhookURL    string `env:"TELEGRAM_WEBHOOK_URL"`
	ProxyURL      string `env:"TELEGRAM_PROXY_URL"`
}

func (c Config) validateTelegram(add func(string, string)) {
	if c.Telegram.ProxyURL != "" {
		proxy, err := url.Parse(c.Telegram.ProxyURL)
		if err != nil || proxy.Scheme != "http" || proxy.Host == "" || proxy.User != nil || proxy.RawQuery != "" || proxy.Fragment != "" || (proxy.Path != "" && proxy.Path != "/") {
			add("TELEGRAM_PROXY_URL", "must be an HTTP proxy URL without credentials, query or path")
		}
	}
	if !c.Telegram.Enabled {
		return
	}
	if mode := c.Telegram.DeliveryMode; mode != "" && mode != "webhook" && mode != "polling" {
		add("TELEGRAM_DELIVERY_MODE", "must be webhook or polling")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`).MatchString(strings.TrimPrefix(c.Telegram.BotName, "@")) {
		add("TELEGRAM_BOT_NAME", "invalid bot username")
	}
	if !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`).MatchString(c.Telegram.BotToken) {
		add("TELEGRAM_BOT_TOKEN", "required bot token")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(c.Telegram.WebhookSecret) {
		add("TELEGRAM_WEBHOOK_SECRET", "must contain 32-256 safe characters")
	}
	u, err := url.Parse(c.Telegram.WebhookURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != BaseURL+"/webhooks/telegram" {
		add("TELEGRAM_WEBHOOK_URL", "must be an HTTPS URL ending in /api/v1/webhooks/telegram")
	}
}
