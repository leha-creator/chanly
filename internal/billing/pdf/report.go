package pdf

import (
	"strconv"

	"github.com/leha-creator/chanly/internal/billing/domain"
)

// AgentReport — отчёт агента за месяц (R-16): размещения с распределением удержания в
// периоде (канал, период показа, выплачено блогеру, вознаграждение) и итоги.
func (r Renderer) AgentReport(doc domain.ReportDoc) ([]byte, error) {
	d := r.start("Отчёт агента " + doc.Number)
	d.heading("Отчёт агента № " + doc.Number + " за " + periodText(doc.Period))
	d.paragraph("Дата составления: " + dateText(doc.IssuedAt))
	d.Ln(2)
	d.labelled("Агент:", supplierText(doc.Supplier))
	d.Ln(1)
	d.labelled("Принципал:", orgText(doc.Principal))
	d.Ln(3)
	d.paragraph("Агент по поручению принципала организовал размещение рекламы в каналах блогеров:")
	d.Ln(2)

	rows := make([][]string, 0, len(doc.Lines))
	for i, l := range doc.Lines {
		rows = append(rows, []string{strconv.Itoa(i + 1), strconv.FormatInt(l.OrderNumber, 10), l.ChannelTitle,
			showPeriod(l), FormatAmount(l.Payout), FormatAmount(l.Fee)})
	}
	d.table([]column{{"№", 10, "C"}, {"Заказ", 18, "C"}, {"Канал", 62, "L"}, {"Период показа", 38, "C"},
		{"Выплачено блогеру", 26, "R"}, {"Вознаграждение", 26, "R"}}, rows)
	placements, fee := domain.ReportTotals(doc.Lines)
	d.Ln(2)
	d.SetFont(family, "B", 10)
	d.CellFormat(contentWidth, lineHeight, "Размещений: "+strconv.Itoa(len(doc.Lines)), "", 1, "R", false, 0, "")
	d.CellFormat(contentWidth, lineHeight, "Выплачено блогерам: "+FormatAmount(placements)+" руб.", "", 1, "R", false, 0, "")
	d.CellFormat(contentWidth, lineHeight, "Вознаграждение агента: "+FormatAmount(fee)+" руб.", "", 1, "R", false, 0, "")
	d.SetFont(family, "", 10)
	d.Ln(2)
	d.paragraph("Вознаграждение агента: " + AmountInWords(fee) + ".")
	d.signatures("Агент", "Принципал")
	return r.finish(d, "agent_report")
}

// showPeriod — даты публикации и снятия; без них — дата распределения.
func showPeriod(l domain.ReportLine) string {
	switch {
	case l.PublishedAt != nil && l.RemovedAt != nil:
		return dateText(*l.PublishedAt) + " – " + dateText(*l.RemovedAt)
	case l.PublishedAt != nil:
		return "с " + dateText(*l.PublishedAt)
	default:
		return dateText(l.SettledAt)
	}
}
