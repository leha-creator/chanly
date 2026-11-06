package httpapi

import (
	"context"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Ежемесячная отчётность ОРД (S-31, R-21): просмотр — обе роли, формирование и отправка — senior.

func (h *Handlers) ListOrdReports(ctx context.Context, _ apigen.ListOrdReportsRequestObject) (apigen.ListOrdReportsResponseObject, error) {
	ctx, _, err := httpx.AdminContext(ctx, h.log, "Marking.ListOrdReports", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.ListReports(ctx)
	if err != nil {
		return nil, err
	}
	out := apigen.ListOrdReports200JSONResponse{Items: []apigen.OrdReportSummary{}}
	for _, v := range list {
		out.Items = append(out.Items, summary(v))
	}
	return out, nil
}

func (h *Handlers) GetOrdReport(ctx context.Context, req apigen.GetOrdReportRequestObject) (apigen.GetOrdReportResponseObject, error) {
	ctx, _, err := httpx.AdminContext(ctx, h.log, "Marking.GetOrdReport", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	period, err := parsePeriod(req.Period)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Report(ctx, period)
	if err != nil {
		return nil, err
	}
	r, err := h.toReport(ctx, v)
	if err != nil {
		return nil, err
	}
	return apigen.GetOrdReport200JSONResponse(r), nil
}

func (h *Handlers) RebuildOrdReport(ctx context.Context, req apigen.RebuildOrdReportRequestObject) (apigen.RebuildOrdReportResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Marking.RebuildOrdReport", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	period, reason, err := periodAndReason(req.Period, req.Body)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.BuildReport(ctx, period, &p.UserID, reason)
	if err != nil {
		return nil, err
	}
	r, err := h.toReport(ctx, v)
	if err != nil {
		return nil, err
	}
	return apigen.RebuildOrdReport200JSONResponse(r), nil
}

func (h *Handlers) SendOrdReport(ctx context.Context, req apigen.SendOrdReportRequestObject) (apigen.SendOrdReportResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Marking.SendOrdReport", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	period, reason, err := periodAndReason(req.Period, req.Body)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.SendReport(ctx, period, &p.UserID, reason)
	if err != nil {
		return nil, err
	}
	r, err := h.toReport(ctx, v)
	if err != nil {
		return nil, err
	}
	return apigen.SendOrdReport200JSONResponse(r), nil
}

func parsePeriod(s string) (domain.Period, error) {
	p, err := domain.ParsePeriod(s)
	if err != nil {
		return domain.Period{}, apperr.Validation("period", "invalid").WithCause(err)
	}
	return p, nil
}

func periodAndReason(period string, body *apigen.AdminReasonInput) (domain.Period, string, error) {
	p, err := parsePeriod(period)
	if err != nil {
		return domain.Period{}, "", err
	}
	if body == nil || strings.TrimSpace(body.Reason) == "" {
		return domain.Period{}, "", apperr.Validation("reason", "required")
	}
	return p, strings.TrimSpace(body.Reason), nil
}

func summary(v *app.ReportView) apigen.OrdReportSummary {
	out := apigen.OrdReportSummary{
		Period: v.Period.String(), State: apigen.OrdReportState(v.State), DueDate: openapi_types.Date{Time: v.Period.DueDate()},
		ErrorCount: v.ErrorCount, PendingCount: v.PendingCount, StatisticsCount: v.StatisticsCount, InvoicesCount: v.InvoicesCount,
	}
	if v.Built != nil {
		out.BuiltAt, out.SendRequestedAt = &v.Built.BuiltAt, v.Built.SendRequestedAt
		imps, amount := v.Impressions, v.Amount
		out.Impressions, out.Amount = &imps, &amount
	}
	return out
}

func (h *Handlers) toReport(ctx context.Context, v *app.ReportView) (apigen.OrdReport, error) {
	s := summary(v)
	out := apigen.OrdReport{
		Period: s.Period, State: s.State, DueDate: s.DueDate, ErrorCount: s.ErrorCount, PendingCount: s.PendingCount,
		StatisticsCount: s.StatisticsCount, InvoicesCount: s.InvoicesCount, BuiltAt: s.BuiltAt, SendRequestedAt: s.SendRequestedAt,
		Impressions: s.Impressions, Amount: s.Amount, Statistics: []apigen.OrdStatisticsRow{}, Invoices: []apigen.OrdInvoiceRow{},
	}
	for _, r := range v.Statistics {
		st := r.Statistics
		out.Statistics = append(out.Statistics, apigen.OrdStatisticsRow{
			Id: st.ID, CreativeId: st.CreativeID, PlacementId: st.PlacementID, Erid: opt(r.Erid),
			Channel: apigen.ChannelRef{Id: r.ChannelID, Title: r.Channel}, Impressions: st.Impressions, Amount: st.Amount,
			ShownFrom: openapi_types.Date{Time: st.ShownFrom}, ShownTo: openapi_types.Date{Time: st.ShownTo},
			Status: apigen.OrdSyncStatus(st.Sync.Status), Attempts: st.Sync.Attempts, Error: opt(st.Sync.Error),
		})
	}
	var c adminref.Collector
	for _, r := range v.Invoices {
		c.Org(r.OrganizationID)
	}
	refs := adminref.Refs{}
	if h.refs != nil {
		var err error
		if refs, err = c.Resolve(ctx, h.refs); err != nil {
			return apigen.OrdReport{}, err
		}
	}
	for _, r := range v.Invoices {
		inv, o := r.Invoice, refs.Org(r.OrganizationID)
		reconcile := inv.Sync.SubmissionUncertain || inv.Links.SubmissionUncertain
		out.Invoices = append(out.Invoices, apigen.OrdInvoiceRow{
			LinksStatus: apigen.OrdSyncStatus(inv.Links.Status), LinksAttempts: inv.Links.Attempts, LinksError: opt(inv.Links.Error),
			DocumentRequestId: opt(inv.Sync.RequestID), LinksRequestId: opt(inv.Links.RequestID), ReconciliationRequired: &reconcile,
			Id: inv.ID, Number: inv.Number, InvoiceDate: openapi_types.Date{Time: inv.InvoiceDate}, Amount: inv.Amount,
			ContractKind: apigen.OrdInvoiceRowContractKind(r.Kind), ItemsCount: len(inv.Items),
			Organization: apigen.AdminOrganizationRef{Id: r.OrganizationID, Name: o.Name, Inn: logx.MaskINN(o.INN)},
			Status:       apigen.OrdSyncStatus(inv.Sync.Status), Attempts: inv.Sync.Attempts, Error: opt(inv.Sync.Error),
		})
	}
	return out, nil
}
