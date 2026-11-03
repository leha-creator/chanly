package httpapi

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// cabinetBlogger — операции модуля доступны кабинету блогера своей организации (07 §4).
const cabinetBlogger = "blogger"

// Handlers — операции OpenAPI выплат (тег blogger-payouts, R-16 этапа выплат).
type Handlers struct {
	svc   *app.Service
	log   *slog.Logger
	admin AdminPorts
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "payout")}
}

// blogger — организация и пользователь с открытым кабинетом блогера.
func blogger(ctx context.Context) (org, user uuid.UUID, err error) {
	p, err := httpx.RequireCabinet(ctx, cabinetBlogger)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if p.OrganizationID == nil {
		return uuid.Nil, uuid.Nil, apperr.Forbidden(httpx.ReasonCabinetRequired).WithDetail("cabinet", cabinetBlogger)
	}
	return *p.OrganizationID, p.UserID, nil
}

// toAPIError переводит ошибки модуля в коды ответа (R-16): юрлицу Рокет Ворк не нужен — 409,
// сервис не подключён — 503. Прочие — как есть (apperr или 500).
func toAPIError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrContractorNotRequired):
		return apperr.Conflict("rocketwork is not required").WithDetail("reason", "contractor_not_required").WithCause(err)
	case errors.Is(err, domain.ErrProviderDisabled), errors.Is(err, domain.ErrProviderUnavailable):
		return apperr.New(apperr.CodeServiceUnavailable, "service is unavailable").
			WithDetail("reason", "rocketwork_unavailable").WithCause(err)
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound("not found").WithCause(err)
	}
	return err
}

func opt[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func statusOf(v app.ContractorView) apigen.RocketworkStatus {
	return apigen.RocketworkStatus{
		Status: apigen.RocketworkContractorStatus(v.Status), Required: v.Required,
		OnboardingUrl: opt(v.OnboardingURL), BlockedReason: opt(v.BlockedReason), StatusCheckedAt: v.StatusCheckedAt,
	}
}

func payoutOf(it app.PayoutItem) apigen.Payout {
	p := it.Payout
	out := apigen.Payout{
		Id: p.ID, PlacementId: p.PlacementID, OrderNumber: it.Brief.OrderNumber,
		Channel: apigen.ChannelRef{Id: it.Brief.ChannelID, Title: it.Brief.ChannelTitle},
		Amount:  int64(p.Amount), Status: apigen.PayoutStatus(p.Status), Method: apigen.PayoutMethod(p.Method),
		Attempt: p.Attempt, ReceiptUrl: opt(p.ReceiptURL), FailureMessage: opt(p.FailureMessage),
		CreatedAt: p.CreatedAt, CompletedAt: p.CompletedAt,
	}
	if p.FailureCode != "" {
		code := apigen.PayoutFailureCode(p.FailureCode)
		out.FailureCode = &code
	}
	return out
}

func (h *Handlers) ListBloggerPayouts(ctx context.Context, req apigen.ListBloggerPayoutsRequestObject) (apigen.ListBloggerPayoutsResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.List(ctx, org, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, toAPIError(err)
	}
	items := make([]apigen.Payout, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, payoutOf(it))
	}
	total := page.Total
	return apigen.ListBloggerPayouts200JSONResponse{Items: items, NextCursor: opt(page.NextCursor), Total: &total}, nil
}

func (h *Handlers) GetBloggerPayoutSummary(ctx context.Context, _ apigen.GetBloggerPayoutSummaryRequestObject) (apigen.GetBloggerPayoutSummaryResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.svc.Summary(ctx, org)
	if err != nil {
		return nil, toAPIError(err)
	}
	h.log.DebugContext(ctx, "payout summary read", logx.KeyOp, "Payout.Summary", logx.KeyOrganizationID, org)
	return apigen.GetBloggerPayoutSummary200JSONResponse{ReceivedAmount: int64(s.Received),
		InProgressAmount: int64(s.InProgress), Count: s.Count}, nil
}

func (h *Handlers) GetRocketworkStatus(ctx context.Context, _ apigen.GetRocketworkStatusRequestObject) (apigen.GetRocketworkStatusResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Status(ctx, org)
	if err != nil {
		return nil, toAPIError(err)
	}
	return apigen.GetRocketworkStatus200JSONResponse(statusOf(v)), nil
}

func (h *Handlers) InviteRocketwork(ctx context.Context, _ apigen.InviteRocketworkRequestObject) (apigen.InviteRocketworkResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.InviteFromCabinet(ctx, org, user)
	if err != nil {
		return nil, toAPIError(err)
	}
	return apigen.InviteRocketwork202JSONResponse(statusOf(v)), nil
}

func (h *Handlers) RefreshRocketworkStatus(ctx context.Context, _ apigen.RefreshRocketworkStatusRequestObject) (apigen.RefreshRocketworkStatusResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.RefreshFromCabinet(ctx, org, user)
	if err != nil {
		return nil, toAPIError(err)
	}
	return apigen.RefreshRocketworkStatus200JSONResponse(statusOf(v)), nil
}
