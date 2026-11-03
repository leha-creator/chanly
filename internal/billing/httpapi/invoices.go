package httpapi

import (
	"context"
	"net/http"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func toInvoice(i domain.Invoice) apigen.Invoice {
	out := apigen.Invoice{
		Id: i.ID, Number: i.Number, Amount: apigen.Money(i.Amount), Status: apigen.InvoiceStatus(i.Status),
		ExpiresAt: i.ExpiresAt, PaidAt: i.PaidAt, CreatedAt: i.CreatedAt,
		PdfUrl: "/api/v1/invoices/" + i.ID.String() + "/pdf",
	}
	if i.PaidAmount != nil {
		out.PaidAmount = (*apigen.Money)(i.PaidAmount)
	}
	return out
}

// CreateInvoice — выставление счёта на пополнение (S-06 шаг 1).
func (h *Handlers) CreateInvoice(ctx context.Context, req apigen.CreateInvoiceRequestObject) (apigen.CreateInvoiceResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := h.svc.CreateInvoice(ctx, org, user, money.Kopecks(req.Body.Amount), httpx.ClientIP(ctx))
	if err != nil {
		return nil, err
	}
	return apigen.CreateInvoice201JSONResponse(toInvoice(inv)), nil
}

func (h *Handlers) ListInvoices(ctx context.Context, req apigen.ListInvoicesRequestObject) (apigen.ListInvoicesResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListInvoices(ctx, org, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Invoice, 0, len(page.Items))
	for _, i := range page.Items {
		items = append(items, toInvoice(i))
	}
	return apigen.ListInvoices200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

// invoicePDF — PDF счёта с номером в имени файла.
type invoicePDF struct{ pdfFile }

func (r invoicePDF) VisitGetInvoicePdfResponse(w http.ResponseWriter) error { return r.write(w) }

func (h *Handlers) GetInvoicePdf(ctx context.Context, req apigen.GetInvoicePdfRequestObject) (apigen.GetInvoicePdfResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	inv, body, size, err := h.svc.InvoicePDF(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return invoicePDF{pdfFile{name: inv.Number, body: body, size: size}}, nil
}
