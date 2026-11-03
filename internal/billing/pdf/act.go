package pdf

import (
	"github.com/leha-creator/chanly/internal/billing/domain"
)

// Act — акт на агентское вознаграждение за месяц (R-16).
func (r Renderer) Act(doc domain.ActDoc) ([]byte, error) {
	d := r.start("Акт " + doc.Number)
	d.heading("Акт № " + doc.Number + " от " + dateText(doc.IssuedAt))
	d.labelled("Исполнитель:", supplierText(doc.Supplier))
	d.Ln(1)
	d.labelled("Заказчик:", orgText(doc.Customer))
	d.Ln(3)
	amount := FormatAmount(doc.FeeAmount)
	d.table([]column{{"№", 10, "C"}, {"Наименование работ, услуг", 104, "L"}, {"Кол-во", 16, "R"}, {"Ед.", 14, "C"},
		{"Сумма", 36, "R"}}, [][]string{
		{"1", "Агентское вознаграждение за " + periodText(doc.Period) + " по отчёту агента № " + doc.Number, "1", "усл.", amount},
	})
	d.Ln(2)
	d.SetFont(family, "B", 10)
	d.CellFormat(contentWidth, lineHeight, "Итого: "+amount, "", 1, "R", false, 0, "")
	d.CellFormat(contentWidth, lineHeight, doc.VATNote, "", 1, "R", false, 0, "")
	d.SetFont(family, "", 10)
	d.Ln(2)
	d.paragraph("Всего оказано услуг 1, на сумму " + amount + " руб.")
	d.SetFont(family, "B", 10)
	d.paragraph(AmountInWords(doc.FeeAmount))
	d.SetFont(family, "", 10)
	d.Ln(3)
	d.paragraph("Вышеперечисленные услуги выполнены полностью и в срок. Заказчик претензий по объёму, качеству и срокам " +
		"оказания услуг не имеет.")
	d.signatures("Исполнитель", "Заказчик")
	return r.finish(d, "act")
}
