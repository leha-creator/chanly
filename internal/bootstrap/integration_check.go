package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

type integrationCheckOptions struct {
	system, scenario, runDir string
	chatID                   int64
	execute                  bool
}

func parseIntegrationCheck(args []string) (integrationCheckOptions, error) {
	var o integrationCheckOptions
	f := flag.NewFlagSet("integration-check", flag.ContinueOnError)
	// flag errors may contain supplied values; never echo arguments into logs.
	f.SetOutput(io.Discard)
	f.StringVar(&o.system, "system", "", "max, ord or rocketwork")
	f.StringVar(&o.scenario, "scenario", "preflight", "scenario")
	f.StringVar(&o.runDir, "run-dir", "", "private directory for this run")
	f.Int64Var(&o.chatID, "chat", 0, "dedicated test channel")
	f.BoolVar(&o.execute, "execute", false, "allow explicit scenario execution")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return o, errUsage
	}
	switch o.system {
	case "max", "ord", "rocketwork":
	default:
		return o, errUsage
	}
	switch o.scenario {
	case "preflight":
		if o.execute {
			return o, errUsage
		}
	case "inspect":
		if o.system != "max" || !o.execute {
			return o, errUsage
		}
	case "publish", "observe", "recover", "remove":
		if o.system != "max" || !o.execute || o.runDir == "" {
			return o, errUsage
		}
	default:
		return o, errUsage
	}
	return o, nil
}

// Preflight deliberately does not open a database or call a provider. A pass is
// configuration readiness only, never evidence of live API compatibility.
func integrationCheckPreflight(c Config, vars map[string]string, o *integrationCheckOptions) error {
	if c.Env != EnvDevelopment {
		return errors.New("integration checks require APP_ENV=development; test uses fakes and production is forbidden")
	}
	switch o.system {
	case "max":
		if c.MAXFake() || strings.TrimSpace(c.MAX.BotToken) == "" {
			return errors.New("MAX_BOT_TOKEN is required; fake cannot pass a live check")
		}
		if !checkEndpoint(c.MAX.APIURL, max.DefaultBaseURL) {
			return errors.New("MAX_API_URL must be the approved MAX endpoint without credentials, query or fragment")
		}
		id, err := strconv.ParseInt(vars["INTEGRATION_CHECK_MAX_CHAT_ID"], 10, 64)
		if err != nil || id == 0 {
			return errors.New("INTEGRATION_CHECK_MAX_CHAT_ID must identify a dedicated test channel")
		}
		if o.chatID != 0 && o.chatID != id {
			return errors.New("requested channel differs from INTEGRATION_CHECK_MAX_CHAT_ID")
		}
		o.chatID = id
		if c.MAX.RateGlobal <= 0 || c.MAX.RateGlobal > 30 || c.MAX.RatePerChat <= 0 || c.MAX.RatePerChat > 2 {
			return errors.New("MAX rate limits must be positive and no greater than 30 global / 2 per chat")
		}
		if _, err := max.NewHTTPClient(c.MAX.CACertFile); err != nil {
			return errors.New("MAX_CA_CERT_FILE could not be loaded; TLS verification remains enabled")
		}
	case "ord":
		if !c.ORD.Enabled || c.ORDFake() || strings.TrimSpace(c.ORD.OAuthToken) == "" {
			return errors.New("ORD_ENABLED and ORD_OAUTH_TOKEN are required; disabled/fake cannot pass")
		}
		if !checkEndpoint(c.ORDBaseURL(), ord.TestBaseURL) {
			return errors.New("ORD_API_URL must be the ORD sandbox endpoint")
		}
		var bad []string
		c.validateORD(func(field, _ string) { bad = append(bad, field) })
		if len(bad) > 0 {
			return fmt.Errorf("ORD configuration incomplete: %s", strings.Join(bad, ", "))
		}
	case "rocketwork":
		return errors.New("rocketwork checks blocked: Q-10/Q-12 API and sandbox contract are unverified")
	}
	return nil
}

func checkEndpoint(raw, expected string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		strings.TrimSuffix(u.String(), "/") == expected
}

func runIntegrationCheck(ctx context.Context, p RunParams) int {
	o, err := parseIntegrationCheck(p.Args)
	if err != nil {
		return 2
	}
	vars := env.ToMap(p.Environ)
	cfg, err := LoadConfig(vars, ModeDev)
	log := logx.New(p.Stdout, slog.LevelInfo, "cli")
	if err != nil {
		// Config errors expose names only, not DSNs or tokens.
		var ce *ConfigError
		if errors.As(err, &ce) {
			log.Error("integration check config invalid", "fields", ce.Fields)
		}
		return 1
	}
	log = integrationCheckLogger(p.Stdout, cfg.Level()).With("op", "IntegrationCheck", "system", o.system, "scenario", o.scenario)
	ctx = logx.WithRequestID(ctx, newCheckRunID())
	log.DebugContext(ctx, "integration check started")
	if err := integrationCheckPreflight(cfg, vars, &o); err != nil {
		log.WarnContext(ctx, "integration check blocked", "reason", err.Error())
		return 1
	}
	if o.scenario == "preflight" {
		log.InfoContext(ctx, "integration preflight passed", "live_verified", false, "network_called", false)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		log.ErrorContext(ctx, "integration check database unavailable")
		return 1
	}
	defer pool.Close()
	in := &infra{pool: pool, clock: clock.System{}, log: log}
	// No full worker bootstrap: no subscriptions, scheduled jobs, mail or unrelated integrations.
	if err = in.connectCheckMAX(cfg); err == nil {
		err = executeMAXCheck(ctx, in.max, in.clock, log, o)
	}
	if err != nil {
		// Provider errors can include raw bodies/URLs. Do not log them here.
		log.ErrorContext(ctx, "integration scenario failed", "error_type", fmt.Sprintf("%T", err))
		return 1
	}
	log.InfoContext(ctx, "integration scenario completed", "full_contract_verified", false)
	return 0
}

// Same adapter and database-backed outbound/limiter as the worker, but no
// inline retries or redirects for an explicitly executed diagnostic scenario.
func (in *infra) connectCheckMAX(cfg Config) error {
	httpClient, err := max.NewHTTPClient(cfg.MAX.CACertFile)
	if err != nil {
		return err
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	out := outbound.New(in.pool, in.clock, in.log, outbound.Options{
		System: outbound.SystemMAX, HTTP: httpClient, NoRetry: true,
	})
	limiter := ratelimit.New(in.pool, in.clock, in.log, ratelimit.Options{})
	in.max = max.New(out, limiter, max.Options{
		BaseURL: cfg.MAX.APIURL, Token: cfg.MAX.BotToken,
		GlobalRate: cfg.MAX.RateGlobal, ChatRate: cfg.MAX.RatePerChat,
	}, in.log)
	return nil
}

func executeMAXCheck(ctx context.Context, api maxAPI, clk clock.Clock, log *slog.Logger, o integrationCheckOptions) error {
	bot, err := api.GetMe(ctx)
	if err != nil {
		return err
	}
	chat, err := api.GetChat(ctx, o.chatID)
	if err != nil {
		return err
	}
	member, err := api.GetMembership(ctx, o.chatID)
	if err != nil {
		return err
	}
	if bot.UserID == 0 || chat.ChatID != o.chatID || chat.Type != max.ChatTypeChannel ||
		member.UserID != bot.UserID || (!member.IsAdmin && !member.IsOwner) {
		log.WarnContext(ctx, "test channel identity or admin membership not confirmed")
		return errors.New("channel identity or membership mismatch")
	}
	if o.scenario == "inspect" {
		messages, err := api.ListMessages(ctx, o.chatID, clk.Now().Add(-24*time.Hour), clk.Now(), 100)
		if err != nil {
			return err
		}
		withViews := 0
		for _, m := range messages {
			if m.Views != nil {
				withViews++
			}
		}
		log.InfoContext(ctx, "max test channel inspected", "bot_id", bot.UserID, "chat_id", o.chatID,
			"messages", len(messages), "messages_with_views", withViews)
		return nil
	}
	// Admin status alone does not grant publication or deletion rights.
	// Require cleanup rights before creating a long-lived test post as well.
	var required []string
	switch o.scenario {
	case "publish":
		required = []string{"write", "delete"}
	case "remove":
		required = []string{"delete"}
	}
	for _, permission := range required {
		if !slices.Contains(member.Permissions, permission) {
			log.WarnContext(ctx, "test mutation blocked by missing permission", "permission", permission)
			return errors.New("required test channel permission missing")
		}
	}
	state, unlock, err := openMAXCheckRun(o.runDir, o.scenario == "publish", bot.UserID, o.chatID, clk.Now())
	if err != nil {
		log.WarnContext(ctx, "run state unavailable; inspect directory and lock before retrying")
		return err
	}
	defer unlock()
	ctx = logx.With(ctx, "run_id", state.RunID)
	log.DebugContext(ctx, "max check run opened", "state", state.Status)
	return advanceMAXCheck(ctx, api, clk, log, o, state)
}

// newCheckRunID is separate from flags: callers cannot inject content into the test post.
func newCheckRunID() string { return uuid.Must(uuid.NewV7()).String() }

// Unverified provider responses may echo data in free-form errors. Keep status,
// operation and correlation, but never print response bodies or error strings.
func integrationCheckLogger(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case "error", "response", "url":
			return slog.Attr{}
		case slog.TimeKey:
			a.Value = slog.TimeValue(a.Value.Time().UTC())
		}
		return a
	}})
	return slog.New(logx.NewContextHandler(h)).With("component", "cli")
}
