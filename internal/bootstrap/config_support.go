package bootstrap

import (
	"net/url"
	"regexp"
	"time"
)

type SupportMAXConfig struct {
	Enabled           bool          `env:"SUPPORT_MAX_ENABLED" envDefault:"false"`
	BotToken          string        `env:"SUPPORT_MAX_BOT_TOKEN"`
	BotUserID         int64         `env:"SUPPORT_MAX_BOT_USER_ID"`
	WebhookURL        string        `env:"SUPPORT_MAX_WEBHOOK_URL"`
	WebhookPathSecret string        `env:"SUPPORT_MAX_WEBHOOK_PATH_SECRET"`
	WebhookSecret     string        `env:"SUPPORT_MAX_WEBHOOK_SECRET"`
	ImageBytes        int64         `env:"SUPPORT_IMAGE_MAX_BYTES" envDefault:"20971520"`
	VideoBytes        int64         `env:"SUPPORT_VIDEO_MAX_BYTES" envDefault:"262144000"`
	AudioBytes        int64         `env:"SUPPORT_AUDIO_MAX_BYTES" envDefault:"52428800"`
	FileBytes         int64         `env:"SUPPORT_FILE_MAX_BYTES" envDefault:"20971520"`
	VoiceSeconds      int           `env:"SUPPORT_VOICE_MAX_SECONDS" envDefault:"600"`
	Retention         time.Duration `env:"SUPPORT_RETENTION" envDefault:"4320h"`
	MediaHosts        []string      `env:"SUPPORT_MEDIA_HOSTS" envSeparator:"," envDefault:"oneme.ru,*.oneme.ru,okcdn.ru,*.okcdn.ru"`
	FFmpeg            string        `env:"SUPPORT_FFMPEG_PATH" envDefault:"ffmpeg"`
}

func (c Config) SupportFake() bool { return c.Env == EnvTest || c.SupportMAX.BotToken == "" }
func (c Config) validateSupport(add func(string, string)) {
	s := c.SupportMAX
	if !s.Enabled {
		return
	}
	if c.Env == EnvProduction && s.BotToken == "" {
		add("SUPPORT_MAX_BOT_TOKEN", "required")
	}
	if s.BotToken != "" {
		if s.BotToken == c.MAX.BotToken {
			add("SUPPORT_MAX_BOT_TOKEN", "must identify a separate bot")
		}
		if s.BotUserID <= 0 {
			add("SUPPORT_MAX_BOT_USER_ID", "positive verified bot ID required")
		}
		if s.BotUserID == c.MAX.BotUserID {
			add("SUPPORT_MAX_BOT_USER_ID", "must differ from primary bot")
		}
		u, e := url.Parse(s.WebhookURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != BaseURL+"/webhooks/max-support" {
			add("SUPPORT_MAX_WEBHOOK_URL", "must be an HTTPS support webhook base URL")
		}
	}
	safe := regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)
	if !safe.MatchString(s.WebhookSecret) {
		add("SUPPORT_MAX_WEBHOOK_SECRET", "32-256 safe characters required")
	}
	if !safe.MatchString(s.WebhookPathSecret) {
		add("SUPPORT_MAX_WEBHOOK_PATH_SECRET", "32-256 safe characters required")
	}
	if s.WebhookSecret == c.MAX.WebhookSecret || s.WebhookPathSecret == c.MAX.WebhookPathSecret {
		add("SUPPORT_MAX_WEBHOOK_SECRET", "must differ from primary webhook secrets")
	}
	if s.ImageBytes <= 0 || s.ImageBytes > 50<<20 || s.VideoBytes <= 0 || s.VideoBytes > 250<<20 || s.AudioBytes <= 0 || s.AudioBytes > 256<<20 || s.FileBytes <= 0 || s.FileBytes > 250<<20 {
		add("SUPPORT_*_MAX_BYTES", "invalid media limit")
	}
	if s.VoiceSeconds <= 0 || s.VoiceSeconds > 3600 || s.Retention <= 0 {
		add("SUPPORT_VOICE_MAX_SECONDS, SUPPORT_RETENTION", "must be positive and bounded")
	}
}
