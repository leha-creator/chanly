package httpapi

import (
	"context"
	"net/http"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/billing/domain"
)

func toClosingAct(a domain.ClosingAct) apigen.ClosingAct {
	base := "/api/v1/closing-acts/" + a.ID.String() + "/"
	return apigen.ClosingAct{
		Id: a.ID, Number: a.Number, Period: a.Period.String(), PlacementsAmount: apigen.Money(a.PlacementsAmount),
		FeeAmount: apigen.Money(a.FeeAmount), CreatedAt: a.CreatedAt,
		ReportUrl: base + string(domain.DocumentReport), ActUrl: base + string(domain.DocumentAct),
	}
}

// ListClosingActs — отчёты агента и акты организации по месяцам (R-16, R-19).
func (h *Handlers) ListClosingActs(ctx context.Context, req apigen.ListClosingActsRequestObject) (apigen.ListClosingActsResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListClosingActs(ctx, org, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ClosingAct, 0, len(page.Items))
	for _, a := range page.Items {
		items = append(items, toClosingAct(a))
	}
	return apigen.ListClosingActs200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

// closingPDF — отчёт агента или акт с периодом и видом документа в имени файла.
type closingPDF struct{ pdfFile }

func (r closingPDF) VisitGetClosingActPdfResponse(w http.ResponseWriter) error { return r.write(w) }

func (h *Handlers) GetClosingActPdf(ctx context.Context, req apigen.GetClosingActPdfRequestObject) (apigen.GetClosingActPdfResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	act, body, size, err := h.svc.ClosingActPDF(ctx, org, req.Id, domain.ClosingDocument(req.Document))
	if err != nil {
		return nil, err
	}
	// Имя файла латиницей: номер документа начинается с кириллической «А».
	return closingPDF{pdfFile{name: "closing-" + act.Period.String() + "-" + string(req.Document), body: body, size: size}}, nil
}
