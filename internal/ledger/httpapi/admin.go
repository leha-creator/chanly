package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Операции администратора журнала проводок (S-19, R-2): сверка, журнал, ручная проводка,
// пополнение депозита, возврат остатка. Только senior (R-1).

// ApprovalRequest — запрос второго подтверждения (модуль admin, bootstrap).
type ApprovalRequest struct {
	Kind    string
	Key     string
	Payload any
	Amount  int64
	Summary string
	Reason  string
}

// Approvals — создание запроса второго подтверждения: ответ 202 ApprovalRequired.
type Approvals interface {
	RequestApproval(ctx context.Context, p *httpx.Principal, r ApprovalRequest) (apigen.AdminApproval, error)
}

// BankStatement — исходящий остаток последней выписки (модуль billing); nil, nil — выписок нет.
type BankStatement interface {
	LatestClosing(ctx context.Context) (closing *money.Kopecks, date *time.Time, err error)
}

// AdminPorts — порты операций администратора.
type AdminPorts struct {
	Approvals Approvals
	Bank      BankStatement
	Refs      adminref.Directory
}

// SetAdminPorts подключает порты (bootstrap).
func (h *Handlers) SetAdminPorts(p AdminPorts) { h.admin = p }

// Виды запросов подтверждения (admin_approval.kind).
const (
	KindLedgerManual = "ledger_manual"
	KindDepositTopup = "deposit_topup"
)

func (h *Handlers) senior(ctx context.Context, op string) (context.Context, *httpx.Principal, error) {
	return httpx.AdminContext(ctx, h.log, op, httpx.AdminSeniorOnly...)
}

// opKey — ключ операции администратора: свой у каждого администратора и вида (R-3).
func opKey(kind string, p *httpx.Principal, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 64 {
		return "", apperr.Validation("Idempotency-Key", "invalid")
	}
	return "http:" + kind + ":" + p.UserID.String() + ":" + key, nil
}

func accountIn(r apigen.LedgerAccountRef) domain.AccountRef {
	return domain.AccountRef{Kind: domain.AccountKind(r.Kind), OrganizationID: deref(r.OrganizationId), PlacementID: deref(r.PlacementId)}
}

func accountOut(r domain.AccountRef) apigen.LedgerAccountRef {
	return apigen.LedgerAccountRef{Kind: apigen.LedgerAccountKind(r.Kind), OrganizationId: opt(r.OrganizationID), PlacementId: opt(r.PlacementID)}
}

// ledgerError — ошибки журнала в ответ API.
func ledgerError(err error) error {
	var neg *domain.NegativeLiabilityError
	switch {
	case errors.As(err, &neg):
		return apperr.Conflict("insufficient balance").WithDetail("reason", "insufficient_balance").
			WithDetail("missing", int64(neg.Missing)).WithCause(err)
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return apperr.Conflict("idempotency key reused with a different operation").
			WithDetail("reason", "idempotency_conflict").WithCause(err)
	case errors.Is(err, domain.ErrInvalidPosting):
		return apperr.Validation("debit", "invalid_account").WithCause(err)
	case errors.Is(err, app.ErrSecondApproval):
		return apperr.Conflict("second approval is required").WithDetail("reason", "second_approval").WithCause(err)
	}
	return err
}

func (h *Handlers) GetLedgerSummary(ctx context.Context, _ apigen.GetLedgerSummaryRequestObject) (apigen.GetLedgerSummaryResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.LedgerSummary")
	if err != nil {
		return nil, err
	}
	s, err := h.svc.AdminSummary(ctx)
	if err != nil {
		return nil, err
	}
	out := apigen.GetLedgerSummary200JSONResponse{Balanced: s.Assets == s.Liabilities,
		AdvertiserLiabilities: int64(s.AdvertiserLiabilities), BankLedgerAmount: int64(s.Totals[domain.AccountBank])}
	for _, k := range domain.AccountKinds {
		out.Accounts = append(out.Accounts, apigen.LedgerAccountTotal{Kind: apigen.LedgerAccountKind(k), Balance: int64(s.Totals[k])})
	}
	if h.admin.Bank != nil {
		closing, date, err := h.admin.Bank.LatestClosing(ctx)
		if err != nil {
			return nil, err
		}
		if closing != nil {
			v, diff := int64(*closing), int64(*closing-s.Totals[domain.AccountBank])
			out.BankStatementAmount, out.BankDifference = &v, &diff
		}
		if date != nil {
			out.BankStatementDate = &openapi_types.Date{Time: *date}
		}
	}
	return out, nil
}

func (h *Handlers) ListLedgerEntries(ctx context.Context, req apigen.ListLedgerEntriesRequestObject) (apigen.ListLedgerEntriesResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.LedgerEntries")
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	q := req.Params
	f := domain.AdminEntryFilter{OrganizationID: q.OrganizationId, Offset: pg.Offset, Limit: pg.Limit}
	if q.Kind != nil {
		k := domain.Kind(*q.Kind)
		f.Kind = &k
	}
	if q.From != nil {
		f.From = adminpage.DayStart(&q.From.Time, 0)
	}
	if q.To != nil {
		f.To = adminpage.DayStart(&q.To.Time, 1)
	}
	rows, total, err := h.svc.AdminEntries(ctx, f)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.LedgerEntry, 0, len(rows))
	for _, e := range rows {
		it := apigen.LedgerEntry{Id: e.TransactionID, Kind: apigen.LedgerTransactionKind(e.Kind), Amount: int64(e.Amount),
			Debit: accountOut(e.Debit), Credit: accountOut(e.Credit), ActorType: apigen.LedgerEntryActorType(e.ActorType),
			ActorUserId: e.ActorUserID, ApproverUserId: opt(e.ApproverID), PlacementId: e.PlacementID,
			InvoiceId: e.InvoiceID, PayoutId: e.PayoutID, CreatedAt: e.CreatedAt.UTC()}
		if e.Reason != "" {
			r := e.Reason
			it.Reason = &r
		}
		items = append(items, it)
	}
	return apigen.ListLedgerEntries200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) CreateManualLedgerEntry(ctx context.Context, req apigen.CreateManualLedgerEntryRequestObject) (apigen.CreateManualLedgerEntryResponseObject, error) {
	const op = "Admin.LedgerManual"
	ctx, p, err := h.senior(ctx, op)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || strings.TrimSpace(b.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	key, err := opKey(KindLedgerManual, p, req.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	debit, credit := accountIn(b.Debit), accountIn(b.Credit)
	if err := app.CheckAccounts(debit, credit); err != nil {
		h.log.WarnContext(ctx, "manual ledger operation rejected", logx.KeyOp, op, "admin_user_id", p.UserID, "reason", "invalid_account")
		return nil, apperr.Validation("debit", "invalid_account").WithCause(err)
	}
	amount := money.Kopecks(b.Amount)
	reason := strings.TrimSpace(b.Reason)
	if h.svc.NeedsApproval(amount) {
		a, err := h.request(ctx, p, ApprovalRequest{Kind: KindLedgerManual, Key: key, Amount: b.Amount, Reason: reason,
			Summary: debit.String() + " → " + credit.String(),
			Payload: app.ManualPayload{Debit: debit, Credit: credit, Amount: amount, Reason: reason}})
		if err != nil {
			return nil, err
		}
		return apigen.CreateManualLedgerEntry202JSONResponse{ApprovalRequiredJSONResponse: apigen.ApprovalRequiredJSONResponse(a)}, nil
	}
	res, err := h.svc.Manual(ctx, app.ManualRequest{Key: key, Debit: debit, Credit: credit, Amount: amount, AdminID: p.UserID, Reason: reason})
	if err != nil {
		return nil, ledgerError(err)
	}
	return apigen.CreateManualLedgerEntry201JSONResponse{TransactionId: res.ID, Replayed: res.Replayed}, nil
}

func (h *Handlers) CreateDepositTopup(ctx context.Context, req apigen.CreateDepositTopupRequestObject) (apigen.CreateDepositTopupResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.DepositTopup")
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || strings.TrimSpace(b.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	key, err := opKey(KindDepositTopup, p, req.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	amount, reason := money.Kopecks(b.Amount), strings.TrimSpace(b.Reason)
	if h.svc.NeedsApproval(amount) {
		a, err := h.request(ctx, p, ApprovalRequest{Kind: KindDepositTopup, Key: key, Amount: b.Amount, Reason: reason,
			Summary: "bank → rocketwork_deposit", Payload: app.TopupPayload{Amount: amount, Reason: reason}})
		if err != nil {
			return nil, err
		}
		return apigen.CreateDepositTopup202JSONResponse{ApprovalRequiredJSONResponse: apigen.ApprovalRequiredJSONResponse(a)}, nil
	}
	res, err := h.svc.DepositTopup(ctx, key, amount, p.UserID, uuid.Nil, reason)
	if err != nil {
		return nil, ledgerError(err)
	}
	return apigen.CreateDepositTopup201JSONResponse{TransactionId: res.ID, Replayed: res.Replayed}, nil
}

func (h *Handlers) CreateBalanceReturn(ctx context.Context, req apigen.CreateBalanceReturnRequestObject) (apigen.CreateBalanceReturnResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.BalanceReturn")
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || strings.TrimSpace(b.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	key, err := opKey("balance_return", p, req.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if h.admin.Refs != nil {
		orgs, err := h.admin.Refs.Orgs(ctx, []uuid.UUID{req.Id})
		if err != nil {
			return nil, err
		}
		if _, ok := orgs[req.Id]; !ok {
			return nil, apperr.NotFound("organization not found")
		}
	}
	res, err := h.svc.BalanceReturn(ctx, key, req.Id, money.Kopecks(b.Amount), p.UserID, strings.TrimSpace(b.Reason))
	if err != nil {
		return nil, ledgerError(err)
	}
	return apigen.CreateBalanceReturn201JSONResponse{TransactionId: res.ID, Replayed: res.Replayed}, nil
}

func (h *Handlers) request(ctx context.Context, p *httpx.Principal, r ApprovalRequest) (apigen.AdminApproval, error) {
	if h.admin.Approvals == nil {
		return apigen.AdminApproval{}, apperr.NotImplemented("second approval")
	}
	return h.admin.Approvals.RequestApproval(ctx, p, r)
}
