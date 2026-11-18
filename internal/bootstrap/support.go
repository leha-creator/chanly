package bootstrap

import (
	"context"
	"encoding/json"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	supportapp "github.com/leha-creator/chanly/internal/support/app"
	"github.com/leha-creator/chanly/internal/support/domain"
	supporthttp "github.com/leha-creator/chanly/internal/support/httpapi"
	supportmedia "github.com/leha-creator/chanly/internal/support/media"
	supportpg "github.com/leha-creator/chanly/internal/support/postgres"
	"net/http"
	"strconv"
)

func (m *modules) wireSupport() {
	cfg := m.cfg.SupportMAX
	limits := domain.DefaultLimits()
	if cfg.ImageBytes > 0 {
		limits.Image = cfg.ImageBytes
	}
	if cfg.VideoBytes > 0 {
		limits.Video = cfg.VideoBytes
	}
	if cfg.AudioBytes > 0 {
		limits.Audio = cfg.AudioBytes
	}
	if cfg.FileBytes > 0 {
		limits.File = cfg.FileBytes
	}
	if cfg.VoiceSeconds > 0 {
		limits.VoiceSeconds = cfg.VoiceSeconds
	}
	log := m.in.log.With("module", "support")
	m.support = &supportapp.Service{Repo: supportpg.New(m.in.pool, log), Tx: m.txRunner, Jobs: m.queue, Clock: m.in.clock, Log: log, Storage: m.in.storage, Provider: bridge.SupportMAX{Client: m.in.supportMAX}, Audit: audit.New(m.in.pool, m.in.clock, log), Enabled: cfg.Enabled, Limits: limits}
	binary := cfg.FFmpeg
	if binary == "" {
		binary = "ffmpeg"
	}
	m.support.Media = supportmedia.Processor{Binary: binary}
	m.support.Fetch = outbound.PublicMediaClient(nil)
	m.support.MediaHosts = cfg.MediaHosts
	m.support.Retention = cfg.Retention
	m.supportHTTP = supporthttp.New(m.support, log)
}

type supportHandlers = supporthttp.Handlers

func (m *modules) supportWebhook() http.Handler {
	if !m.cfg.SupportMAX.Enabled {
		return http.NotFoundHandler()
	}
	cfg := m.cfg.SupportMAX
	rec := inbound.NewRecorder(m.in.pool, m.txRunner, m.queue, m.in.clock, m.in.log)
	return supporthttp.NewWebhook(cfg.WebhookPathSecret, cfg.WebhookSecret, strconv.FormatInt(cfg.BotUserID, 10), rec, m.in.log)
}
func (m *modules) registerSupportInbound() {
	if !m.cfg.SupportMAX.Enabled {
		return
	}
	for _, kind := range []string{"message_created", "message_edited", "message_removed"} {
		m.platform.dispatcher.Handle(supporthttp.Source, kind, func(ctx context.Context, e inbound.Event) error {
			var in domain.Incoming
			if json.Unmarshal(e.Payload, &in) != nil {
				return inbound.ErrIgnore
			}
			return m.support.Ingest(ctx, in)
		})
	}
}
