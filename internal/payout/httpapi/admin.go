package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Реестр выплат администратора (adm/payouts, R-2, R-14): только senior (R-1).

// ApprovalRequest — запрос второго подтверждения (модуль admin, bootstrap).
type ApprovalRequest struct {
	Kind     string
	Key      string
	Payload  any
	Amount   int64
	Summary  string
	Reason   string
	ObjectID uuid.UUID
}

// Approvals — создание запроса второго подтверждения: ответ 202 ApprovalRequired.
type Approvals interface {
	RequestApproval(ctx context.Context, p *httpx.Principal, r ApprovalRequest) (apigen.AdminApproval, error)
}

// AdminPorts — порты операций администратора.
type AdminPorts struct {
	Approvals Approvals
	Refs      adminref.Directory
}

// SetAdminPorts подключает порты (bootstrap).
func (h *Handlers) SetAdminPorts(p AdminPorts) { h.admin = p }

// KindPayoutMarkPaid — вид запроса подтверждения отметки выплаты.
const KindPayoutMarkPaid = "payout_mark_paid"

var moscow = time.FixedZone("MSK", 3*60*60)

func (h *Handlers) senior(ctx context.Context, op string) (context.Context, *httpx.Principal, error) {
	return httpx.AdminContext(ctx, h.log, op, httpx.AdminSeniorOnly...)
}

// adminError — ошибки модуля для операций администратора.
func adminError(err error) error {
	switch {
	case errors.Is(err, domain.ErrPayoutNotRetryable):
		return apperr.Conflict("payout is not retryable").WithDetail("reason", "not_retryable").WithCause(err)
	case errors.Is(err, domain.ErrInvalidPayoutState):
		return apperr.Conflict("payout state does not allow the operation").WithDetail("reason", "invalid_state").WithCause(err)
	case errors.Is(err, domain.ErrSecondApproval):
		return apperr.Conflict("second approval is required").WithDetail("reason", "second_approval").WithCause(err)
	case errors.Is(err, domain.ErrInvalidPayout):
		return apperr.Validation("reason", "required").WithCause(err)
	}
	return toAPIError(err)
}

func (h *Handlers) refs(ctx context.Context, items []app.PayoutItem) (adminref.Refs, error) {
	if h.admin.Refs == nil {
		return adminref.Refs{}, nil
	}
	var c adminref.Collector
	for _, it := range items {
		c.Org(it.BloggerOrganizationID)
	}
	return c.Resolve(ctx, h.admin.Refs)
}

// adminPayoutOf — выплата в ответе администратора: ИНН блогера с маской (D-3).
func adminPayoutOf(r adminref.Refs, it app.PayoutItem) apigen.AdminPayout {
	base := payoutOf(it)
	o := r.Org(it.BloggerOrganizationID)
	return apigen.AdminPayout{Id: base.Id, PlacementId: base.PlacementId, OrderNumber: base.OrderNumber, Channel: base.Channel,
		Amount: base.Amount, Status: base.Status, Method: base.Method, Attempt: base.Attempt, ReceiptUrl: base.ReceiptUrl,
		FailureCode: base.FailureCode, FailureMessage: base.FailureMessage, CreatedAt: base.CreatedAt.UTC(),
		CompletedAt: base.CompletedAt, BankReference: opt(it.BankReference),
		Blogger: apigen.AdminOrganizationRef{Id: it.BloggerOrganizationID, Name: o.Name, Inn: logx.MaskINN(o.INN)}}
}

func (h *Handlers) adminPayouts(ctx context.Context, items []app.PayoutItem) ([]apigen.AdminPayout, error) {
	refs, err := h.refs(ctx, items)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.AdminPayout, 0, len(items))
	for _, it := range items {
		out = append(out, adminPayoutOf(refs, it))
	}
	return out, nil
}

// PlacementPayouts — выплаты размещения для карточки администратора (порт placement/httpapi.AdminPayouts):
// видит только senior (R-1), moderator — пустой список.
func (h *Handlers) PlacementPayouts(ctx context.Context, p *httpx.Principal, placementID uuid.UUID) ([]apigen.AdminPayout, bool, error) {
	if !httpx.IsSenior(p) {
		return []apigen.AdminPayout{}, false, nil
	}
	items, retryable, err := h.svc.PlacementPayouts(ctx, placementID)
	if err != nil {
		return nil, false, err
	}
	out, err := h.adminPayouts(ctx, items)
	return out, retryable, err
}

func filterOf(status *apigen.PayoutStatus, org *uuid.UUID, from, to *time.Time) domain.AdminFilter {
	var f domain.AdminFilter
	if status != nil {
		f.Status = domain.Status(*status)
	}
	if org != nil {
		f.OrganizationID = *org
	}
	if t := adminpage.DayStart(from, 0); t != nil {
		f.From = *t
	}
	if t := adminpage.DayStart(to, 1); t != nil {
		f.To = *t
	}
	return f
}

func datePtr(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}

func (h *Handlers) ListAdminPayouts(ctx context.Context, req apigen.ListAdminPayoutsRequestObject) (apigen.ListAdminPayoutsResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.ListPayouts")
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	q := req.Params
	f := filterOf(q.Status, q.OrganizationId, datePtr(q.From), datePtr(q.To))
	f.Offset, f.Limit = pg.Offset, pg.Limit
	items, total, err := h.svc.AdminPage(ctx, f)
	if err != nil {
		return nil, adminError(err)
	}
	out, err := h.adminPayouts(ctx, items)
	if err != nil {
		return nil, err
	}
	return apigen.ListAdminPayouts200JSONResponse{Items: out, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) GetAdminPayoutSummary(ctx context.Context, _ apigen.GetAdminPayoutSummaryRequestObject) (apigen.GetAdminPayoutSummaryResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.PayoutSummary")
	if err != nil {
		return nil, err
	}
	s, err := h.svc.AdminSummary(ctx)
	if err != nil {
		return nil, adminError(err)
	}
	out := apigen.GetAdminPayoutSummary200JSONResponse{ByStatus: []apigen.AdminPayoutStatusTotal{},
		DepositLedgerAmount: int64(s.DepositLedger), PayableAmount: int64(max(s.Payable, 0)),
		InProgressAmount: int64(s.InProgress), ShortageAmount: int64(s.Shortage)}
	for _, t := range s.ByStatus {
		out.ByStatus = append(out.ByStatus, apigen.AdminPayoutStatusTotal{Status: apigen.PayoutStatus(t.Status), Count: t.Count,
			Amount: int64(t.Amount)})
	}
	return out, nil
}

// csvHeader — колонки выгрузки реестра (R-14): ИНН с маской.
var csvHeader = []string{"Создана (МСК)", "Блогер", "ИНН", "Заказ", "Канал", "Попытка", "Способ", "Сумма, ₽",
	"Состояние", "Код ошибки", "Платёжное поручение", "Завершена (МСК)", "Выплата", "Размещение"}

func (h *Handlers) ExportAdminPayouts(ctx context.Context, req apigen.ExportAdminPayoutsRequestObject) (apigen.ExportAdminPayoutsResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.ExportPayouts")
	if err != nil {
		return nil, err
	}
	q := req.Params
	items, err := h.svc.ExportRows(ctx, filterOf(q.Status, q.OrganizationId, datePtr(q.From), datePtr(q.To)), p.UserID)
	if err != nil {
		return nil, adminError(err)
	}
	refs, err := h.refs(ctx, items)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM: Excel открывает UTF-8
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	_ = w.Write(csvHeader)
	for _, it := range items {
		o := refs.Org(it.BloggerOrganizationID)
		_ = w.Write([]string{mskTime(&it.CreatedAt), csvSafe(o.Name), logx.MaskINN(o.INN), strconv.FormatInt(it.Brief.OrderNumber, 10),
			csvSafe(it.Brief.ChannelTitle), strconv.Itoa(it.Attempt), string(it.Method), rubles(it.Amount), string(it.Status),
			string(it.FailureCode), csvSafe(it.BankReference), mskTime(it.CompletedAt), it.ID.String(), it.PlacementID.String()})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return apigen.ExportAdminPayouts200TextcsvResponse{Body: &buf, ContentLength: int64(buf.Len())}, nil
}

// csvSafe — текст пользователя в ячейке не исполняется таблицей как формула (CSV injection):
// значение, начинающееся с = + - @ или управляющего символа, получает ведущий апостроф.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func mskTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(moscow).Format("02.01.2006 15:04")
}

// rubles — «1234,56» без float (CSV для таблиц).
func rubles(k money.Kopecks) string {
	v, sign := int64(k), ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d,%02d", sign, v/100, v%100)
}

func (h *Handlers) RetryPayout(ctx context.Context, req apigen.RetryPayoutRequestObject) (apigen.RetryPayoutResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.RetryPayout")
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	next, err := h.svc.Retry(ctx, req.Id, p.UserID, req.Body.Reason)
	if err != nil {
		return nil, adminError(err)
	}
	items, err := h.svc.PlacementPayoutItem(ctx, next)
	if err != nil {
		return nil, err
	}
	out, err := h.adminPayouts(ctx, items)
	if err != nil {
		return nil, err
	}
	return apigen.RetryPayout200JSONResponse(out[0]), nil
}

func (h *Handlers) MarkPayoutPaid(ctx context.Context, req apigen.MarkPayoutPaidRequestObject) (apigen.MarkPayoutPaidResponseObject, error) {
	const op = "Admin.MarkPayoutPaid"
	ctx, p, err := h.senior(ctx, op)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || strings.TrimSpace(b.Reason) == "" || strings.TrimSpace(b.BankReference) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	key := strings.TrimSpace(req.Params.IdempotencyKey)
	if key == "" || len(key) > 64 {
		return nil, apperr.Validation("Idempotency-Key", "invalid")
	}
	cur, err := h.svc.Payout(ctx, req.Id)
	if err != nil {
		return nil, adminError(err)
	}
	if cur.Method != domain.MethodBankTransfer {
		return nil, apperr.Conflict("payout is paid by rocketwork").WithDetail("reason", "invalid_state")
	}
	reason, ref := strings.TrimSpace(b.Reason), strings.TrimSpace(b.BankReference)
	if h.svc.NeedsApproval(cur.Amount) && cur.Status != domain.StatusSucceeded {
		if h.admin.Approvals == nil {
			return nil, apperr.NotImplemented("second approval")
		}
		a, err := h.admin.Approvals.RequestApproval(ctx, p, ApprovalRequest{Kind: KindPayoutMarkPaid,
			Key: "http:" + KindPayoutMarkPaid + ":" + p.UserID.String() + ":" + key, Amount: int64(cur.Amount),
			Summary: "blogger_payable → bank, " + ref, Reason: reason, ObjectID: cur.ID,
			Payload: app.MarkPaidPayload{PayoutID: cur.ID, BankReference: ref, Reason: reason}})
		if err != nil {
			return nil, err
		}
		return apigen.MarkPayoutPaid202JSONResponse{ApprovalRequiredJSONResponse: apigen.ApprovalRequiredJSONResponse(a)}, nil
	}
	paid, err := h.svc.MarkLegalPaid(ctx, req.Id, p.UserID, uuid.Nil, ref, reason)
	if err != nil {
		return nil, adminError(err)
	}
	items, err := h.svc.PlacementPayoutItem(ctx, paid)
	if err != nil {
		return nil, err
	}
	out, err := h.adminPayouts(ctx, items)
	if err != nil {
		return nil, err
	}
	return apigen.MarkPayoutPaid200JSONResponse(out[0]), nil
}
