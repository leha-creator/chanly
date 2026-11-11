package pdf

import (
	"github.com/leha-creator/chanly/internal/billing/domain"
)

// Invoice — счёт на пополнение баланса (R-10): банковские реквизиты получателя, номер и дата,
// поставщик, плательщик (без банковских реквизитов — только наименование и ИНН, D-26), строка
// услуги, сумма цифрами и прописью, строка НДС и срок действия.
func (r Renderer) Invoice(doc domain.InvoiceDoc) ([]byte, error) {
	d := r.start("Счёт " + doc.Number)
	s := doc.Supplier

	// Платёжный блок получателя — как в образце платёжного поручения.
	d.SetFont(family, "", 9)
	d.table([]column{{"Банк получателя", 110, "L"}, {"Реквизиты", 70, "L"}}, [][]string{
		{s.BankName, "БИК " + s.BankBIK + "\nСч. № " + s.BankCorrAccount},
		{"Получатель: " + s.Name + "\nИНН " + s.INN, "Сч. № " + s.BankAccount},
	})
	d.Ln(4)

	d.heading("Счёт на оплату № " + doc.Number + " от " + dateText(doc.IssuedAt))
	d.labelled("Поставщик:", supplierText(s))
	d.Ln(1)
	payer := doc.Payer.Name + ", ИНН " + doc.Payer.INN
	if doc.Payer.HasBankDetails() {
		payer += ", р/с " + doc.Payer.BankAccount + " в " + doc.Payer.BankName + ", БИК " + doc.Payer.BankBIK
		if doc.Payer.BankCorrAccount != "" {
			payer += ", к/с " + doc.Payer.BankCorrAccount
		}
	}
	d.labelled("Покупатель:", payer)
	d.Ln(3)

	amount := FormatAmount(doc.Amount)
	d.table([]column{{"№", 10, "C"}, {"Наименование", 96, "L"}, {"Кол-во", 16, "R"}, {"Ед.", 14, "C"},
		{"Цена", 22, "R"}, {"Сумма", 22, "R"}}, [][]string{
		{"1", "Пополнение баланса по счёту № " + doc.Number, "1", "усл.", amount, amount},
	})
	d.Ln(2)
	d.SetFont(family, "B", 10)
	d.CellFormat(contentWidth, lineHeight, "Итого: "+amount, "", 1, "R", false, 0, "")
	d.CellFormat(contentWidth, lineHeight, doc.VATNote, "", 1, "R", false, 0, "")
	d.CellFormat(contentWidth, lineHeight, "Всего к оплате: "+amount, "", 1, "R", false, 0, "")
	d.SetFont(family, "", 10)
	d.Ln(2)
	d.paragraph("Всего наименований 1, на сумму " + amount + " руб.")
	d.SetFont(family, "B", 10)
	d.paragraph(AmountInWords(doc.Amount))
	d.SetFont(family, "", 10)
	d.Ln(3)
	if doc.PaymentPurpose != "" {
		d.paragraph("Назначение платежа: " + doc.PaymentPurpose)
		d.paragraph("Скопируйте назначение платежа полностью, включая код после номера счёта.")
	}
	d.paragraph("Счёт действителен до " + dateText(doc.ExpiresAt) + " включительно. В назначении платежа укажите номер счёта " +
		doc.Number + ". Оплата третьими лицами не допускается.")
	d.signatures("Индивидуальный предприниматель", "")
	return r.finish(d, "invoice")
}
