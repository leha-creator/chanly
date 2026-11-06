package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// inspectCheckORD verifies sandbox access using a new, non-existent organization ID.
// It never creates objects, opens the worker, or retries requests inline.
func (in *infra) inspectCheckORD(ctx context.Context, cfg Config) error {
	started := in.clock.Now()
	runID := newCheckRunID()
	in.log.DebugContext(ctx, "ord access inspection started", "run_id", runID)
	client := in.checkORDClient(cfg)
	_, err := client.GetOrganizationStatus(ctx, "access-check-"+runID)
	if !errors.Is(err, ord.ErrOrganizationNotFound) {
		in.log.WarnContext(ctx, "ord access inspection inconclusive", "run_id", runID)
		if err == nil {
			return errors.New("unexpected existing access-check organization")
		}
		return err
	}
	in.log.InfoContext(ctx, "ord sandbox read access verified", "run_id", runID,
		"http_status", http.StatusNotFound, "authorization_verified", true, "writes_performed", false,
		"full_contract_verified", false, "elapsed_ms", in.clock.Now().Sub(started)/time.Millisecond)
	return nil
}

func (in *infra) checkORDClient(cfg Config) *ord.Client {
	httpClient := outbound.NewHTTPClient(nil)
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	out := outbound.New(in.pool, in.clock, in.log, outbound.Options{
		System: outbound.SystemYandexORD, HTTP: httpClient, NoRetry: true,
		Redact: []string{"inn", "name", "address", "textData", "data", "detail", "error_message"},
	})
	limiter := ratelimit.New(in.pool, in.clock, in.log, ratelimit.Options{})
	return ord.New(out, limiter, ord.Options{BaseURL: cfg.ORDBaseURL(), APIVersion: cfg.ORD.APIVersion,
		Token: cfg.ORD.OAuthToken, Rate: cfg.ORD.Rate}, in.log)
}
