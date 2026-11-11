package bootstrap

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

// Normal dev API holds only a capability for one installed diagnostic run.
// No provider token, control DSN or policy credentials cross this boundary.
type rwCheckProxy struct {
	cfg   Config
	log   *slog.Logger
	audit audit.Recorder
}

func (h *rwCheckProxy) call(ctx context.Context, method, expectedRun string) (apigen.RocketworkCheck, error) {
	ctx, _, authErr := httpx.AdminContext(ctx, h.log, "RocketWorkCheck", httpx.AdminSeniorOnly...)
	if authErr != nil {
		return apigen.RocketworkCheck{}, authErr
	}
	if method == http.MethodPost {
		if _, err := uuid.Parse(expectedRun); err != nil {
			return apigen.RocketworkCheck{}, apperr.Validation("If-Match", "invalid")
		}
	}
	if h.cfg.Env != EnvDevelopment || h.cfg.RocketWork.CheckGatewayURL == "" {
		if method == http.MethodGet {
			return apigen.RocketworkCheck{Available: h.cfg.Env == EnvDevelopment, State: "not_configured"}, nil
		}
		return apigen.RocketworkCheck{}, apperr.New(apperr.CodeServiceUnavailable, "check is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, h.cfg.RocketWork.CheckGatewayURL+"/check", nil)
	if err != nil {
		return apigen.RocketworkCheck{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.RocketWork.CheckGatewayToken)
	if method == http.MethodPost {
		if h.audit == nil {
			return apigen.RocketworkCheck{}, apperr.New(apperr.CodeServiceUnavailable, "audit is unavailable")
		}
		if err := h.audit.Record(ctx, audit.Entry{Action: "rocketwork_check.ui_requested", ObjectType: "rw_check_run", ObjectID: expectedRun, ActorType: audit.ActorAdmin}); err != nil {
			return apigen.RocketworkCheck{}, err
		}
		req.Header.Set("If-Match", expectedRun)
	}
	client := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return apigen.RocketworkCheck{}, apperr.New(apperr.CodeServiceUnavailable, "check is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apigen.RocketworkCheck{}, apperr.New(apperr.CodeServiceUnavailable, "check is unavailable")
	}
	var view apigen.RocketworkCheck
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8192))
	if decoder.Decode(&view) != nil {
		return view, apperr.New(apperr.CodeServiceUnavailable, "check is unavailable")
	}
	return view, nil
}

func (h *rwCheckProxy) GetAdminRocketworkCheck(ctx context.Context, _ apigen.GetAdminRocketworkCheckRequestObject) (apigen.GetAdminRocketworkCheckResponseObject, error) {
	v, err := h.call(ctx, http.MethodGet, "")
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminRocketworkCheck200JSONResponse(v), nil
}
func (h *rwCheckProxy) StartAdminRocketworkCheck(ctx context.Context, request apigen.StartAdminRocketworkCheckRequestObject) (apigen.StartAdminRocketworkCheckResponseObject, error) {
	expected := ""
	if request.Params.IfMatch != nil {
		expected = *request.Params.IfMatch
	}
	v, err := h.call(ctx, http.MethodPost, expected)
	if err != nil {
		return nil, err
	}
	return apigen.StartAdminRocketworkCheck200JSONResponse(v), nil
}
