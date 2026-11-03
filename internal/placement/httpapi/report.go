package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/placement/domain"
)

// GetOrderReport — отчёт по кампании (S-09, R-14 этапа «Уведомления и отчёт по кампании»).
func (h *Handlers) GetOrderReport(ctx context.Context, req apigen.GetOrderReportRequestObject) (apigen.GetOrderReportResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.OrderReport(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetOrderReport200JSONResponse(toReport(r)), nil
}

// GetOrderReportCsv — тот же отчёт для Excel (R-15).
func (h *Handlers) GetOrderReportCsv(ctx context.Context, req apigen.GetOrderReportCsvRequestObject) (apigen.GetOrderReportCsvResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.OrderReport(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := domain.WriteReportCSV(&b, r); err != nil {
		return nil, err
	}
	return reportCSV{name: fmt.Sprintf("order-%d-report.csv", r.Order.Number), body: b.Bytes()}, nil
}

// reportCSV — ответ CSV с именем файла: сгенерированный ответ не задаёт Content-Disposition.
type reportCSV struct {
	name string
	body []byte
}

func (r reportCSV) VisitGetOrderReportCsvResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, r.name))
	w.Header().Set("Content-Length", fmt.Sprint(len(r.body)))
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, bytes.NewReader(r.body))
	return err
}

func toReport(r domain.Report) apigen.OrderReport {
	t := r.Totals
	out := apigen.OrderReport{OrderId: r.Order.ID, OrderNumber: r.Order.Number, CampaignName: r.CampaignName, Links: r.Links,
		Utm: apigen.OrderReportUtm{Source: r.UTM.Source, Medium: r.UTM.Medium, Campaign: r.UTM.Campaign},
		Totals: apigen.OrderReportTotals{Placements: t.Placements, Published: t.Published, Views: t.Views,
			SpentAmount: apigen.Money(t.Spent), RefundedAmount: apigen.Money(t.Refunded), HeldAmount: apigen.Money(t.Held),
			CpvAmount: moneyPtr(t.CPV)},
		Rows: make([]apigen.OrderReportRow, 0, len(r.Rows))}
	for _, row := range r.Rows {
		campaign := r.UTM.Campaign
		out.Rows = append(out.Rows, apigen.OrderReportRow{PlacementId: row.PlacementID, Channel: channelRef(row.Channel),
			Status: apigen.PlacementStatus(row.Status), SpentAmount: apigen.Money(row.Spent),
			RefundedAmount: apigen.Money(row.Refunded), HeldAmount: apigen.Money(row.Held), PublishedAt: utcPtr(row.PublishedAt),
			RemovedAt: utcPtr(row.RemovedAt), Views: row.Views, PublicationUrl: opt(row.PublicationURL), UtmCampaign: &campaign,
			CpvAmount: moneyPtr(row.CPV)})
	}
	return out
}
