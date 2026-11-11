package httpapi

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Оплаты и счета администратора (adm/payments, S-30): только senior (R-1). ИНН плательщика —
// полный (сверка, R-14), ИНН в ссылке на организацию — с маской (D-3).

// AdminPorts — порты операций администратора.
type AdminPorts struct {
	Refs adminref.Directory
	// StatementMaxBytes — предел файла выписки (STATEMENT_MAX_BYTES).
	StatementMaxBytes int
}

// SetAdminPorts подключает порты (bootstrap).
func (h *Handlers) SetAdminPorts(p AdminPorts) { h.admin = p }

func (h *Handlers) senior(ctx context.Context, op string) (context.Context, *httpx.Principal, error) {
	return httpx.AdminContext(ctx, h.log, op, httpx.AdminSeniorOnly...)
}

func (h *Handlers) resolve(ctx context.Context, c *adminref.Collector) (adminref.Refs, error) {
	if h.admin.Refs == nil {
		return adminref.Refs{}, nil
	}
	return c.Resolve(ctx, h.admin.Refs)
}

func orgRef(r adminref.Refs, id uuid.UUID) apigen.AdminOrganizationRef {
	o := r.Org(id)
	return apigen.AdminOrganizationRef{Id: id, Name: o.Name, Inn: logx.MaskINN(o.INN)}
}

// statementError — отказ по самому файлу выписки.
func statementError(err error) error {
	switch {
	case errors.Is(err, domain.ErrStatementImported):
		return apperr.Conflict("statement already imported").WithDetail("reason", "statement_already_imported").WithCause(err)
	case errors.Is(err, domain.ErrStatementAccount):
		return apperr.Validation("file", "another_account").WithCause(err)
	case errors.Is(err, domain.ErrStatementTooLarge):
		return apperr.Validation("file", "too_large").WithCause(err)
	case errors.Is(err, domain.ErrStatementFormat):
		return apperr.Validation("file", "format").WithCause(err)
	}
	return err
}

func (h *Handlers) statementSummary(ctx context.Context, p *httpx.Principal, st domain.BankStatement) (apigen.BankStatementSummary, error) {
	var c adminref.Collector
	if st.UploadedBy != uuid.Nil {
		c.User(&st.UploadedBy)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return apigen.BankStatementSummary{}, err
	}
	out := apigen.BankStatementSummary{Id: st.ID, AccountNumber: st.Account, IncomingCount: st.IncomingCount,
		MatchedCount: st.MatchedCount, UnmatchedCount: st.UnmatchedCount, DuplicateCount: st.DuplicateCount,
		Source:    apigen.BankStatementSummarySource(st.Source),
		CreatedAt: st.CreatedAt.UTC()}
	if st.Source == "" {
		out.Source = apigen.File
	}
	if st.UploadedBy != uuid.Nil {
		out.UploadedBy = &apigen.AdminUserRef{Id: st.UploadedBy, Email: httpx.EmailFor(p, refs.Emails[st.UploadedBy])}
	}
	if st.DateFrom != nil {
		out.DateFrom = &openapi_types.Date{Time: *st.DateFrom}
	}
	if st.DateTo != nil {
		out.DateTo = &openapi_types.Date{Time: *st.DateTo}
	}
	if st.Opening != nil {
		v := int64(*st.Opening)
		out.OpeningBalance = &v
	}
	if st.Closing != nil {
		v := int64(*st.Closing)
		out.ClosingBalance = &v
	}
	return out, nil
}

func (h *Handlers) UploadBankStatement(ctx context.Context, req apigen.UploadBankStatementRequestObject) (apigen.UploadBankStatementResponseObject, error) {
	const op = "Admin.UploadBankStatement"
	ctx, p, err := h.senior(ctx, op)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apperr.Validation("file", "required")
	}
	var data []byte
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperr.Validation("file", "invalid").WithCause(err)
		}
		if part.FormName() != "file" {
			continue
		}
		limit := int64(h.admin.StatementMaxBytes)
		if limit <= 0 {
			limit = 10 << 20
		}
		if data, err = io.ReadAll(io.LimitReader(part, limit+1)); err != nil {
			return nil, apperr.Validation("file", "invalid").WithCause(err)
		}
		if int64(len(data)) > limit {
			h.log.WarnContext(ctx, "bank statement rejected", logx.KeyOp, op, "reason", "too_large", "admin_user_id", p.UserID)
			return nil, apperr.Validation("file", "too_large")
		}
		break
	}
	if len(data) == 0 {
		return nil, apperr.Validation("file", "required")
	}
	res, err := h.svc.ImportStatement(ctx, p.UserID, data)
	if err != nil {
		if app.IsStatementRejection(err) {
			return nil, statementError(err)
		}
		// Сбой после части поступлений: зачисленное остаётся, остаток — выпиской за следующий период или вручную.
		return nil, err
	}
	st, err := h.svc.Statement(ctx, res.StatementID)
	if err != nil {
		return nil, err
	}
	out, err := h.statementSummary(ctx, p, st)
	if err != nil {
		return nil, err
	}
	return apigen.UploadBankStatement201JSONResponse(out), nil
}

func (h *Handlers) GetLatestBankStatement(ctx context.Context, _ apigen.GetLatestBankStatementRequestObject) (apigen.GetLatestBankStatementResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.LatestBankStatement")
	if err != nil {
		return nil, err
	}
	st, err := h.svc.LatestUploadedStatement(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.statementSummary(ctx, p, st)
	if err != nil {
		return nil, err
	}
	return apigen.GetLatestBankStatement200JSONResponse(out), nil
}

func bankTransactionOf(r adminref.Refs, bt domain.BankTransaction, invoices map[uuid.UUID]domain.Invoice) apigen.BankTransaction {
	out := apigen.BankTransaction{Id: bt.ID, OperationDate: openapi_types.Date{Time: bt.OperationDate}, Amount: int64(bt.Amount),
		PayerName: opt(bt.PayerName), PayerInn: opt(bt.PayerINN), PayerAccount: opt(bt.PayerAccount), Purpose: opt(bt.Purpose),
		MatchStatus: apigen.BankMatchStatus(bt.MatchStatus), IgnoredReason: opt(bt.IgnoredReason), CreatedAt: bt.CreatedAt.UTC()}
	if bt.OrganizationID != uuid.Nil {
		ref := orgRef(r, bt.OrganizationID)
		out.Organization = &ref
	}
	if inv, ok := invoices[bt.InvoiceID]; ok {
		out.InvoiceNumber = &inv.Number
	}
	if bt.MatchStatus == domain.MatchMatched {
		manual := bt.MatchedBy != uuid.Nil
		out.MatchedManually = &manual
	}
	return out
}

func (h *Handlers) bankTransactions(ctx context.Context, list []domain.BankTransaction, invoices map[uuid.UUID]domain.Invoice) ([]apigen.BankTransaction, error) {
	var c adminref.Collector
	for _, bt := range list {
		if bt.OrganizationID != uuid.Nil {
			c.Org(bt.OrganizationID)
		}
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.BankTransaction, 0, len(list))
	for _, bt := range list {
		out = append(out, bankTransactionOf(refs, bt, invoices))
	}
	return out, nil
}

func (h *Handlers) ListBankTransactions(ctx context.Context, req apigen.ListBankTransactionsRequestObject) (apigen.ListBankTransactionsResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.ListBankTransactions")
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	var status domain.MatchStatus
	if req.Params.MatchStatus != nil {
		status = domain.MatchStatus(*req.Params.MatchStatus)
	}
	list, invoices, total, err := h.svc.AdminBankTransactions(ctx, status, pg.Offset, pg.Limit)
	if err != nil {
		return nil, err
	}
	items, err := h.bankTransactions(ctx, list, invoices)
	if err != nil {
		return nil, err
	}
	return apigen.ListBankTransactions200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) bankTransactionResponse(ctx context.Context, bt domain.BankTransaction) (apigen.BankTransaction, error) {
	invoices, err := h.svc.BankTransactionInvoice(ctx, bt)
	if err != nil {
		return apigen.BankTransaction{}, err
	}
	items, err := h.bankTransactions(ctx, []domain.BankTransaction{bt}, invoices)
	if err != nil {
		return apigen.BankTransaction{}, err
	}
	return items[0], nil
}

func (h *Handlers) MatchBankTransaction(ctx context.Context, req apigen.MatchBankTransactionRequestObject) (apigen.MatchBankTransactionResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.MatchBankTransaction")
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || strings.TrimSpace(b.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	bt, err := h.svc.MatchManually(ctx, req.Id, b.OrganizationId, deref(b.InvoiceId), p.UserID, strings.TrimSpace(b.Reason))
	if err != nil {
		return nil, err
	}
	out, err := h.bankTransactionResponse(ctx, bt)
	if err != nil {
		return nil, err
	}
	return apigen.MatchBankTransaction200JSONResponse(out), nil
}

func (h *Handlers) IgnoreBankTransaction(ctx context.Context, req apigen.IgnoreBankTransactionRequestObject) (apigen.IgnoreBankTransactionResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.IgnoreBankTransaction")
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	bt, err := h.svc.Ignore(ctx, req.Id, p.UserID, strings.TrimSpace(req.Body.Reason))
	if err != nil {
		return nil, err
	}
	out, err := h.bankTransactionResponse(ctx, bt)
	if err != nil {
		return nil, err
	}
	return apigen.IgnoreBankTransaction200JSONResponse(out), nil
}

func adminInvoiceOf(r adminref.Refs, inv domain.Invoice) apigen.AdminInvoice {
	base := toInvoice(inv)
	return apigen.AdminInvoice{Id: base.Id, Number: base.Number, Amount: base.Amount, Status: base.Status,
		ExpiresAt: base.ExpiresAt.UTC(), PaidAt: base.PaidAt, PaidAmount: base.PaidAmount, CreatedAt: base.CreatedAt.UTC(),
		PdfUrl: base.PdfUrl, CancelReason: opt(inv.CancelReason), Organization: orgRef(r, inv.OrganizationID)}
}

func (h *Handlers) adminInvoices(ctx context.Context, list []domain.Invoice) ([]apigen.AdminInvoice, error) {
	var c adminref.Collector
	for _, inv := range list {
		c.Org(inv.OrganizationID)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.AdminInvoice, 0, len(list))
	for _, inv := range list {
		out = append(out, adminInvoiceOf(refs, inv))
	}
	return out, nil
}

func (h *Handlers) ListAdminInvoices(ctx context.Context, req apigen.ListAdminInvoicesRequestObject) (apigen.ListAdminInvoicesResponseObject, error) {
	ctx, _, err := h.senior(ctx, "Admin.ListInvoices")
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	f := domain.AdminInvoiceFilter{OrganizationID: req.Params.OrganizationId, Number: strings.TrimSpace(deref(req.Params.Number)),
		Offset: pg.Offset, Limit: pg.Limit}
	if req.Params.Status != nil {
		f.Status = domain.InvoiceStatus(*req.Params.Status)
	}
	list, total, err := h.svc.AdminInvoices(ctx, f)
	if err != nil {
		return nil, err
	}
	items, err := h.adminInvoices(ctx, list)
	if err != nil {
		return nil, err
	}
	return apigen.ListAdminInvoices200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) CancelAdminInvoice(ctx context.Context, req apigen.CancelAdminInvoiceRequestObject) (apigen.CancelAdminInvoiceResponseObject, error) {
	ctx, p, err := h.senior(ctx, "Admin.CancelInvoice")
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	inv, err := h.svc.CancelInvoice(ctx, req.Id, p.UserID, strings.TrimSpace(req.Body.Reason))
	if err != nil {
		return nil, err
	}
	items, err := h.adminInvoices(ctx, []domain.Invoice{inv})
	if err != nil {
		return nil, err
	}
	return apigen.CancelAdminInvoice200JSONResponse(items[0]), nil
}
