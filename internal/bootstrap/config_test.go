package bootstrap

import (
	"bytes"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func validAPIEnv() map[string]string {
	return map[string]string{
		"APP_ENV":            "development",
		"DATABASE_URL":       "postgres://maxim_app:secret-pass@localhost:5432/maxim",
		"S3_ENDPOINT":        "localhost:9000",
		"S3_BUCKET_PRIVATE":  "maxim-private",
		"S3_BUCKET_PUBLIC":   "maxim-public",
		"S3_PUBLIC_BASE_URL": "http://localhost:9000/maxim-public",
		"S3_ACCESS_KEY":      "minio",
		"S3_SECRET_KEY":      "minio-secret",
		"APP_SECRETS_KEY":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"AUTH_CODE_PEPPER":   strings.Repeat("p", 32),
	}
}

func configFields(t *testing.T, err error) []string {
	t.Helper()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConfigError, got %v", err)
	}
	return ce.Fields
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	a := cfg.Auth
	if a.LoginCodeTTL != 10*time.Minute || a.LoginCodeAttempts != 5 || a.CodesPerEmailHour != 5 ||
		a.CodesPerIPHour != 20 || a.SessionTTL != 30*24*time.Hour || a.AdminSessionTTL != 12*time.Hour {
		t.Fatalf("auth defaults differ from 07 §4: %+v", a)
	}
	if cfg.Level() != slog.LevelDebug {
		t.Fatalf("development default level = %v, want debug", cfg.Level())
	}
	if len(cfg.SecretsKeyBytes()) != 32 {
		t.Fatal("secrets key is not 32 bytes")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		edit  func(map[string]string)
		field string
	}{
		{"missing database", ModeAPI, func(m map[string]string) { delete(m, "DATABASE_URL") }, "DATABASE_URL: required"},
		{"short secrets key", ModeAPI, func(m map[string]string) { m["APP_SECRETS_KEY"] = base64.StdEncoding.EncodeToString([]byte("short")) }, "APP_SECRETS_KEY: must be 32 bytes in base64"},
		{"short pepper", ModeAPI, func(m map[string]string) { m["AUTH_CODE_PEPPER"] = "x" }, "AUTH_CODE_PEPPER: must be at least 32 characters"},
		{"production without secure cookie", ModeAPI, func(m map[string]string) { m["APP_ENV"] = "production" }, "COOKIE_SECURE: must be true in production"},
		{"unknown env", ModeAPI, func(m map[string]string) { m["APP_ENV"] = "staging" }, "APP_ENV: must be development, test or production"},
		{"bad log level", ModeAPI, func(m map[string]string) { m["LOG_LEVEL"] = "verbose" }, "LOG_LEVEL: must be debug, info, warn or error"},
		{"missing storage", ModeAPI, func(m map[string]string) { delete(m, "S3_ENDPOINT") }, "S3_ENDPOINT: required"},
		{"bad proxy cidr", ModeAPI, func(m map[string]string) { m["TRUSTED_PROXY_CIDRS"] = "10.0.0.0/8,nope" }, "TRUSTED_PROXY_CIDRS: invalid CIDR"},
		{"worker without smtp", ModeWorker, func(m map[string]string) {}, "SMTP_HOST: required"},
		{"bad duration", ModeAPI, func(m map[string]string) { m["AUTH_SESSION_TTL"] = "month" }, "AUTH_SESSION_TTL: invalid value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validAPIEnv()
			c.edit(e)
			_, err := LoadConfig(e, c.mode)
			fields := configFields(t, err)
			if !slices.Contains(fields, c.field) {
				t.Fatalf("fields %v do not contain %q", fields, c.field)
			}
			if strings.Contains(err.Error(), "secret-pass") {
				t.Fatal("error text leaks a secret")
			}
		})
	}
}

func TestMigrateNeedsOnlyDatabase(t *testing.T) {
	if _, err := LoadConfig(map[string]string{"DATABASE_URL": "postgres://x@y/z"}, ModeMigrate); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestConfigLogValueHidesSecrets(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	out := buf.String()
	for _, secret := range []string{"secret-pass", "minio-secret", cfg.SecretsKey, cfg.AuthCodePepper} {
		if strings.Contains(out, secret) {
			t.Fatalf("log contains secret %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "maxim_app:***@localhost") {
		t.Fatalf("database url is not redacted as expected: %s", out)
	}
}

func validWorkerEnv() map[string]string {
	e := validAPIEnv()
	e["SMTP_HOST"] = "localhost"
	e["MAIL_FROM"] = "noreply@maxim.test"
	return e
}

func TestWorkerConfig(t *testing.T) {
	cfg, err := LoadConfig(validWorkerEnv(), ModeWorker)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MAX.RateGlobal != 25 || cfg.MAX.RatePerChat != 1 || cfg.Scheduler.DowntimeThreshold != 30*time.Minute ||
		cfg.Scheduler.StuckInboundEventAge != 10*time.Minute || cfg.Retention.OutboundRequest != 180*24*time.Hour {
		t.Fatalf("defaults differ from 05 §2.8, 07 §2–§3, 04 §8: %+v %+v %+v", cfg.MAX, cfg.Scheduler, cfg.Retention)
	}
	if !cfg.MAXFake() {
		t.Fatal("development without MAX_BOT_TOKEN must use the fake adapter")
	}

	pemFile := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(pemFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		edit  func(map[string]string)
		field string
	}{
		{"production without bot token", func(m map[string]string) { m["APP_ENV"] = "production"; m["ALERT_EMAILS"] = "oncall@maxim.ru" }, "MAX_BOT_TOKEN: required in production"},
		{"production without alert recipients", func(m map[string]string) { m["APP_ENV"] = "production"; m["MAX_BOT_TOKEN"] = "t" },
			"ALERT_EMAILS: at least one alert recipient (ALERT_EMAILS or ALERT_MAX_CHAT_ID) is required in production"},
		{"bad pem", func(m map[string]string) { m["MAX_CA_CERT_FILE"] = pemFile }, "MAX_CA_CERT_FILE: no PEM certificates found"},
		{"missing pem", func(m map[string]string) { m["MAX_CA_CERT_FILE"] = pemFile + ".missing" }, "MAX_CA_CERT_FILE: cannot be read"},
		{"zero rate", func(m map[string]string) { m["MAX_RATE_GLOBAL"] = "0" }, "MAX_RATE_*: must be positive"},
		{"bad alert email", func(m map[string]string) { m["ALERT_EMAILS"] = "oncall@maxim.ru,nope" }, "ALERT_EMAILS: invalid address"},
		{"zero retention", func(m map[string]string) { m["RETENTION_SESSION"] = "0s" }, "RETENTION_*: must be positive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validWorkerEnv()
			c.edit(e)
			_, err := LoadConfig(e, ModeWorker)
			if fields := configFields(t, err); !slices.Contains(fields, c.field) {
				t.Fatalf("fields %v do not contain %q", fields, c.field)
			}
		})
	}
}

func TestConfigLogValueHidesBotToken(t *testing.T) {
	e := validWorkerEnv()
	e["MAX_BOT_TOKEN"] = "max-bot-secret"
	cfg, err := LoadConfig(e, ModeWorker)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	if strings.Contains(buf.String(), "max-bot-secret") {
		t.Fatal("log contains MAX bot token")
	}
	if cfg.MAXFake() {
		t.Fatal("development with MAX_BOT_TOKEN must use the live adapter")
	}
}

func TestChannelConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	ch := cfg.Channel
	if ch.LinkCodeTTL != time.Hour || ch.LinkCodesPerHour != 10 || ch.UnclaimedTTL != 7*24*time.Hour || ch.RknThreshold != 10000 {
		t.Fatalf("channel defaults differ from A-41, D-11: %+v", ch)
	}
	long := strings.Repeat("s", 32)
	cases := []struct {
		name  string
		mode  string
		edit  func(map[string]string)
		field string
	}{
		{"short path secret", ModeAPI, func(m map[string]string) { m["MAX_WEBHOOK_PATH_SECRET"] = "short" },
			"MAX_WEBHOOK_PATH_SECRET: must be at least 32 characters"},
		{"production api without webhook secret", ModeAPI, func(m map[string]string) {
			m["APP_ENV"], m["COOKIE_SECURE"], m["MAX_WEBHOOK_PATH_SECRET"] = "production", "true", long
		}, "MAX_WEBHOOK_SECRET: required in production"},
		{"zero code ttl", ModeAPI, func(m map[string]string) { m["CHANNEL_LINK_CODE_TTL"] = "0s" },
			"CHANNEL_*, RKN_SUBSCRIBERS_THRESHOLD: must be positive"},
		{"production worker with http webhook url", ModeWorker, func(m map[string]string) {
			m["SMTP_HOST"], m["MAIL_FROM"], m["APP_ENV"] = "smtp", "noreply@maxim.ru", "production"
			m["MAX_BOT_TOKEN"], m["ALERT_EMAILS"] = "t", "oncall@maxim.ru"
			m["MAX_WEBHOOK_URL"] = "http://api.maxim.ru/api/v1/webhooks/max"
		}, "MAX_WEBHOOK_URL: must be an https URL in production"},
		{"production worker without webhook secrets", ModeWorker, func(m map[string]string) {
			m["SMTP_HOST"], m["MAIL_FROM"], m["APP_ENV"] = "smtp", "noreply@maxim.ru", "production"
			m["MAX_BOT_TOKEN"], m["ALERT_EMAILS"] = "t", "oncall@maxim.ru"
			m["MAX_WEBHOOK_URL"] = "https://api.maxim.ru/api/v1/webhooks/max"
		}, "MAX_WEBHOOK_PATH_SECRET: required in production"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validAPIEnv()
			c.edit(e)
			_, err := LoadConfig(e, c.mode)
			if fields := configFields(t, err); !slices.Contains(fields, c.field) {
				t.Fatalf("fields %v do not contain %q", fields, c.field)
			}
		})
	}

	e := validAPIEnv()
	e["MAX_WEBHOOK_PATH_SECRET"], e["MAX_WEBHOOK_SECRET"] = "path-"+long, "header-"+long
	cfg, err = LoadConfig(e, ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	if strings.Contains(buf.String(), long) {
		t.Fatal("log contains webhook secrets")
	}
}

func TestCatalogConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	ch := cfg.Channel
	if ch.FeeRateBP != 2500 || ch.RknRecheckInterval != 2160*time.Hour || ch.MetricsSubscribersSpikeBP != 2000 ||
		ch.MetricsViewsMinBP != 100 || ch.UnavailabilityMaxDays != 90 || ch.UnavailabilityHorizonDays != 365 ||
		ch.UnavailabilityMaxCount != 50 || cfg.Retention.ChannelUnavailability != 720*time.Hour {
		t.Fatalf("catalog defaults: %+v", ch)
	}
	for _, tc := range []struct{ key, value, field string }{
		{"PLATFORM_FEE_RATE_BP", "10001", "PLATFORM_FEE_RATE_BP"},
		{"PLATFORM_FEE_RATE_BP", "-1", "PLATFORM_FEE_RATE_BP"},
		{"METRICS_SUBSCRIBERS_SPIKE_BP", "-5", "METRICS_SUBSCRIBERS_SPIKE_BP"},
		{"METRICS_VIEWS_MIN_BP", "-1", "METRICS_VIEWS_MIN_BP"},
		{"CHANNEL_UNAVAILABILITY_MAX_COUNT", "0", "CHANNEL_UNAVAILABILITY_*"},
		{"RKN_RECHECK_INTERVAL", "-1h", "RKN_RECHECK_INTERVAL"},
	} {
		env := validAPIEnv()
		env[tc.key] = tc.value
		_, err := LoadConfig(env, ModeAPI)
		if err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, tc.field) }) {
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
	}
	if _, err := LoadConfig(map[string]string{"DATABASE_URL": "postgres://x"}, ModeAdmin); err != nil {
		t.Errorf("admin mode needs only the database: %v", err)
	}
}

func TestMediaConfig(t *testing.T) {
	cfg, err := LoadConfig(validWorkerEnv(), ModeWorker)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Media
	if m.MaxBytes != 50<<20 || m.MinSide != 160 || m.PreviewSide != 640 || m.UploadsPerHour != 60 || m.UploadConcurrency != 4 ||
		m.BuildConcurrency != 2 || m.AlbumLimit != 10 || m.URLTTL != time.Hour || m.RetentionDeleted != 30*24*time.Hour ||
		m.PostBodyLimit != 3800 || m.PostFinalTextLimit != 4000 {
		t.Fatalf("defaults differ from R-2, R-6, R-19, A-23, A-38: %+v", m)
	}
	for _, tt := range []struct {
		mode, key, value, field string
	}{
		{ModeAPI, "MEDIA_MAX_BYTES", "52428801", "MEDIA_MAX_BYTES"},
		{ModeAPI, "MEDIA_ALBUM_LIMIT", "11", "MEDIA_ALBUM_LIMIT"},
		{ModeAPI, "POST_BODY_LIMIT", "4000", "POST_BODY_LIMIT"},
		{ModeAPI, "POST_FINAL_TEXT_LIMIT", "4001", "POST_FINAL_TEXT_LIMIT"},
		{ModeWorker, "MEDIA_BUILD_CONCURRENCY", "0", "MEDIA_UPLOADS_PER_HOUR, MEDIA_*_CONCURRENCY"},
		{ModeAPI, "S3_BUCKET_PUBLIC", "", "S3_BUCKET_PUBLIC"},
		{ModeWorker, "S3_BUCKET_PUBLIC", "", "S3_BUCKET_PUBLIC"},
		{ModeWorker, "S3_ENDPOINT", "", "S3_ENDPOINT"},
		{ModeAPI, "S3_PUBLIC_BASE_URL", "not a url", "S3_PUBLIC_BASE_URL"},
	} {
		env := validWorkerEnv()
		env[tt.key] = tt.value
		_, err := LoadConfig(env, tt.mode)
		if err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, tt.field+":") }) {
			t.Errorf("%s=%q (%s): want error on %s, got %v", tt.key, tt.value, tt.mode, tt.field, err)
		}
	}
	// legal публичная корзина не нужна; в тестовом окружении хранилище поддельное.
	env := validAPIEnv()
	delete(env, "S3_BUCKET_PUBLIC")
	if _, err := LoadConfig(env, ModeLegal); err != nil {
		t.Fatalf("legal without public bucket: %v", err)
	}
	env = map[string]string{"APP_ENV": "test", "DATABASE_URL": "postgres://x@localhost/x",
		"APP_SECRETS_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	if _, err := LoadConfig(env, ModeWorker); err != nil {
		t.Fatalf("test worker without S3: %v", err)
	}
	// В продакшене публичный адрес — только https.
	prod := validWorkerEnv()
	prod["APP_ENV"] = "production"
	_, err = LoadConfig(prod, ModeAPI)
	if err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, "S3_PUBLIC_BASE_URL:") }) {
		t.Fatalf("http public url in production: %v", err)
	}
}
