// Package httpapi — операции OpenAPI модуля администратора: запросы второго подтверждения (R-2),
// журнал действий (R-15), счётчики меню (R-16). Роли — матрица R-1.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/admin/app"
	"github.com/leha-creator/chanly/internal/admin/domain"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Handlers — операции модуля.
type Handlers struct {
	svc  *app.Service
	refs adminref.Directory
	log  *slog.Logger
}

// New создаёт обработчики; refs — почта администраторов в ответах.
func New(svc *app.Service, refs adminref.Directory, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, refs: refs, log: log.With(logx.KeyModule, "admin")}
}

func (h *Handlers) resolve(ctx context.Context, c *adminref.Collector) (adminref.Refs, error) {
	if h.refs == nil {
		return adminref.Refs{}, nil
	}
	return c.Resolve(ctx, h.refs)
}

func userRef(p *httpx.Principal, r adminref.Refs, id *uuid.UUID) *apigen.AdminUserRef {
	if id == nil {
		return nil
	}
	return &apigen.AdminUserRef{Id: *id, Email: httpx.EmailFor(p, r.Emails[*id])}
}

func approvalOf(p *httpx.Principal, r adminref.Refs, a domain.Approval) apigen.AdminApproval {
	out := apigen.AdminApproval{Id: a.ID, Kind: apigen.AdminApprovalKind(a.Kind), Status: apigen.AdminApprovalStatus(a.Status),
		Amount: a.Amount, Summary: a.Summary, Reason: a.Reason, RequestedBy: *userRef(p, r, &a.RequestedBy),
		DecidedBy: userRef(p, r, a.DecidedBy), ObjectId: a.ObjectID, ResultId: a.ResultID,
		CreatedAt: a.CreatedAt.UTC(), ExpiresAt: a.ExpiresAt.UTC()}
	if a.DecisionReason != "" {
		out.DecisionReason = &a.DecisionReason
	}
	if a.DecidedAt != nil {
		t := a.DecidedAt.UTC()
		out.DecidedAt = &t
	}
	return out
}

func (h *Handlers) approvals(ctx context.Context, p *httpx.Principal, list ...domain.Approval) ([]apigen.AdminApproval, error) {
	var c adminref.Collector
	for _, a := range list {
		c.User(&a.RequestedBy)
		c.User(a.DecidedBy)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.AdminApproval, len(list))
	for i, a := range list {
		out[i] = approvalOf(p, refs, a)
	}
	return out, nil
}

// RequestApproval — запрос второго подтверждения от операции другого модуля (ledger, payout):
// ответ 202 ApprovalRequired. Повтор ключа — тот же запрос.
func (h *Handlers) RequestApproval(ctx context.Context, p *httpx.Principal, r domain.Request) (apigen.AdminApproval, error) {
	r.AdminID = p.UserID
	a, _, err := h.svc.RequestApproval(ctx, r)
	if err != nil {
		return apigen.AdminApproval{}, err
	}
	out, err := h.approvals(ctx, p, a)
	if err != nil {
		return apigen.AdminApproval{}, err
	}
	return out[0], nil
}

func (h *Handlers) ListAdminApprovals(ctx context.Context, req apigen.ListAdminApprovalsRequestObject) (apigen.ListAdminApprovalsResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Admin.ListApprovals", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	var st *domain.Status
	if req.Params.Status != nil {
		v := domain.Status(*req.Params.Status)
		if !v.Valid() {
			return nil, apperr.Validation("status", "invalid")
		}
		st = &v
	}
	list, total, err := h.svc.List(ctx, st, domain.Page{Offset: pg.Offset, Limit: pg.Limit})
	if err != nil {
		return nil, err
	}
	items, err := h.approvals(ctx, p, list...)
	if err != nil {
		return nil, err
	}
	return apigen.ListAdminApprovals200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) ApproveAdminApproval(ctx context.Context, req apigen.ApproveAdminApprovalRequestObject) (apigen.ApproveAdminApprovalResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Admin.ApproveApproval", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	a, err := h.svc.Approve(ctx, req.Id, p.UserID)
	if err != nil {
		return nil, err
	}
	out, err := h.approvals(ctx, p, a)
	if err != nil {
		return nil, err
	}
	return apigen.ApproveAdminApproval200JSONResponse(out[0]), nil
}

func (h *Handlers) RejectAdminApproval(ctx context.Context, req apigen.RejectAdminApprovalRequestObject) (apigen.RejectAdminApprovalResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Admin.RejectApproval", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apperr.Validation("reason", "required")
	}
	a, err := h.svc.Reject(ctx, req.Id, p.UserID, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	out, err := h.approvals(ctx, p, a)
	if err != nil {
		return nil, err
	}
	return apigen.RejectAdminApproval200JSONResponse(out[0]), nil
}

func (h *Handlers) ListAuditLog(ctx context.Context, req apigen.ListAuditLogRequestObject) (apigen.ListAuditLogResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Admin.ListAuditLog", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	q := req.Params
	f := app.AuditFilter{ActorUserID: q.ActorUserId, Action: q.Action, ObjectType: q.ObjectType, ObjectID: q.ObjectId,
		OrganizationID: q.OrganizationId, ManualOnly: q.ManualOnly != nil && *q.ManualOnly}
	if q.ActorType != nil {
		v := string(*q.ActorType)
		f.ActorType = &v
	}
	if q.From != nil {
		f.From = adminpage.DayStart(&q.From.Time, 0)
	}
	if q.To != nil {
		f.To = adminpage.DayStart(&q.To.Time, 1)
	}
	list, total, err := h.svc.AuditLog(ctx, f, domain.Page{Offset: pg.Offset, Limit: pg.Limit})
	if err != nil {
		return nil, err
	}
	var c adminref.Collector
	for _, e := range list {
		c.User(e.ActorUserID)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AuditLogEntry, 0, len(list))
	for _, e := range list {
		data := map[string]any{}
		if len(e.Data) > 0 {
			if err := json.Unmarshal(e.Data, &data); err != nil {
				data = map[string]any{}
			}
		}
		items = append(items, apigen.AuditLogEntry{Id: e.ID, ActorType: apigen.AuditLogEntryActorType(e.ActorType),
			Actor: userRef(p, refs, e.ActorUserID), OrganizationId: e.OrganizationID, Action: e.Action,
			ObjectType: e.ObjectType, ObjectId: e.ObjectID, Reason: e.Reason, Data: data, CreatedAt: e.CreatedAt.UTC()})
	}
	return apigen.ListAuditLog200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) GetAdminCounters(ctx context.Context, _ apigen.GetAdminCountersRequestObject) (apigen.GetAdminCountersResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Admin.Counters", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Counters(ctx)
	if err != nil {
		return nil, err
	}
	if !httpx.IsSenior(p) {
		// Очереди только senior (выписка, выплаты, подтверждения) модератору не показываются.
		c.BankTransactionsUnmatched, c.PayoutsFailed, c.ApprovalsPending = 0, 0, 0
	}
	return apigen.GetAdminCounters200JSONResponse{ChannelsModeration: c.ChannelsModeration, OrdersModeration: c.OrdersModeration,
		DisputesOpen: c.DisputesOpen, DisputesOverdue: c.DisputesOverdue, OrdErrors: c.OrdErrors,
		BankTransactionsUnmatched: c.BankTransactionsUnmatched, PayoutsFailed: c.PayoutsFailed,
		ApprovalsPending: c.ApprovalsPending, OrdReportPending: c.OrdReportPending}, nil
}
