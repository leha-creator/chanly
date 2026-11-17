package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"

	"github.com/leha-creator/chanly/internal/billing/domain"
)

// Invoice renders our A4 invoice without the recipient's registration address.
func (r Renderer) Invoice(doc domain.InvoiceDoc) ([]byte, error) {
	d := r.start("Счёт " + doc.Number)
	s := doc.Supplier
	d.SetFont(family, "B", 17)
	d.MultiCell(contentWidth, 8, "Счёт на пополнение баланса", "", "L", false)
	d.Ln(1)
	d.SetFont(family, "", 10)
	d.CellFormat(100, 6, "№ "+doc.Number+" от "+dateText(doc.IssuedAt), "", 0, "L", false, 0, "")
	d.CellFormat(contentWidth-100, 6, "Оплатить до "+dateText(doc.ExpiresAt), "", 1, "R", false, 0, "")
	d.SetFont(family, "", 9)
	d.SetTextColor(90, 90, 90)
	d.paragraph("На перечисление средств рекламодателя для исполнения агентского поручения")
	d.SetTextColor(0, 0, 0)
	d.Ln(6)

	purpose := doc.PaymentPurpose
	if purpose == "" {
		purpose = fmt.Sprintf("Средства принципала на исполнение поручения по оферте Chanly. Счёт № %s от %s. %s", doc.Number, dateText(doc.IssuedAt), doc.VATNote)
	}
	qr, err := invoiceQR(doc, purpose)
	if err != nil {
		return nil, err
	}
	bankY := d.GetY()
	d.SetDrawColor(180, 180, 180)
	d.SetLineWidth(0.2)
	d.table([]column{{"Банк получателя", 80, "L"}, {"Реквизиты", 52, "L"}}, [][]string{
		{s.BankName, "БИК " + s.BankBIK},
		{"Корреспондентский счёт", s.BankCorrAccount},
		{"Расчётный счёт", s.BankAccount},
	})
	bankBottom := d.GetY()
	d.SetXY(margin, bankBottom+3)
	d.SetFont(family, "B", 9)
	d.CellFormat(25, 4.5, "Получатель:", "", 0, "L", false, 0, "")
	d.SetFont(family, "", 9)
	d.MultiCell(107, 4.5, s.Name, "", "L", false)
	d.SetX(margin)
	recipient := "ИНН " + s.INN
	if s.OGRNIP != "" {
		recipient += ", ОГРНИП " + s.OGRNIP
	}
	d.MultiCell(132, 4.5, recipient+"\nEmail: support@chanly.ru", "", "L", false)
	bankBottom = d.GetY()
	if len(qr) != 0 {
		const qrSize = 40.0
		qrX := pageWidth - margin - qrSize
		d.RegisterImageOptionsReader("invoice-payment", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(qr))
		d.ImageOptions("invoice-payment", qrX, bankY-2, qrSize, qrSize, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
		d.SetXY(qrX, bankY+qrSize-2)
		d.SetFont(family, "", 8)
		d.CellFormat(qrSize, 4, "Оплата по реквизитам", "", 0, "C", false, 0, "")
		bankBottom = max(bankBottom, bankY+qrSize+2)
	}
	d.SetXY(margin, bankBottom+5)
	d.SetFont(family, "", 10)

	payer := doc.Payer.Name + ", ИНН " + doc.Payer.INN
	if doc.PayerKPP != "" {
		payer += ", КПП " + doc.PayerKPP
	}
	if doc.PayerAddress != "" {
		payer += ", " + doc.PayerAddress
	}
	if doc.Payer.HasBankDetails() {
		payer += ", р/с " + doc.Payer.BankAccount + " в " + doc.Payer.BankName + ", БИК " + doc.Payer.BankBIK
		if doc.Payer.BankCorrAccount != "" {
			payer += ", к/с " + doc.Payer.BankCorrAccount
		}
	}
	d.labelled("Плательщик:", payer)
	d.Ln(2)
	d.labelled("Основание:", "Оферта рекламодателя Chanly: https://chanly.ru/offer")
	if doc.OrganizationID != uuid.Nil {
		d.SetFont(family, "", 8)
		d.SetTextColor(90, 90, 90)
		d.paragraph("ID рекламодателя: " + doc.OrganizationID.String())
		d.SetTextColor(0, 0, 0)
	}
	d.Ln(5)

	amount := FormatAmount(doc.Amount)
	d.table([]column{{"№", 8, "C"}, {"Назначение платежа", 96, "L"}, {"Кол.", 14, "C"},
		{"Цена, руб.", 31, "R"}, {"Сумма, руб.", 31, "R"}}, [][]string{
		{"1", purpose, "1", amount, amount},
	})
	d.Ln(4)
	d.SetFont(family, "B", 13)
	d.CellFormat(contentWidth, 7, "Итого к оплате: "+amount+" руб.", "", 1, "R", false, 0, "")
	d.Ln(1)
	d.SetFont(family, "", 10)
	d.paragraph("Сумма прописью: " + AmountInWords(doc.Amount))
	d.Ln(2)
	d.SetFont(family, "", 9)
	d.paragraph(doc.VATNote + ". Счёт выставлен на перечисление средств принципала в рамках агентского договора (оферты).")
	d.Ln(3)
	d.SetFont(family, "", 8)
	d.paragraph("В платёжном поручении укажите назначение из таблицы полностью. Зачисление на баланс - после поступления и сопоставления платежа по выписке.")

	d.invoiceSpace(43)
	d.Ln(7)
	d.SetFont(family, "", 10)
	d.paragraph("Индивидуальный предприниматель")
	d.paragraph(strings.TrimPrefix(s.Name, "ИП "))
	d.Ln(2)
	d.CellFormat(120, 6, "Подпись: ____________________", "", 0, "L", false, 0, "")
	d.CellFormat(contentWidth-120, 6, "М.П. (при наличии)", "", 1, "R", false, 0, "")
	d.Ln(5)
	d.SetFont(family, "", 8)
	d.SetTextColor(90, 90, 90)
	d.paragraph("Счёт на оплату не является счётом-фактурой, актом или отчётом агента. Оплата и отражение средств на балансе выполняются по оферте рекламодателя Chanly.")
	return r.finish(d, "invoice")
}

func (d document) invoiceSpace(height float64) {
	_, pageHeight := d.GetPageSize()
	if d.GetY()+height > pageHeight-margin {
		d.AddPage()
	}
}

// invoiceQR encodes exactly the printed purpose and the amount in kopecks.
// Requisites that cannot be represented omit the optional QR entirely.
func invoiceQR(doc domain.InvoiceDoc, purpose string) ([]byte, error) {
	s := doc.Supplier
	values := []string{s.Name, s.BankAccount, s.BankName, s.BankBIK, s.BankCorrAccount, s.INN, purpose}
	for _, value := range values {
		if value == "" || strings.ContainsAny(value, "|\r\n") {
			return nil, nil
		}
	}
	if doc.Amount <= 0 {
		return nil, nil
	}
	payload := "ST00012|Name=" + s.Name + "|PersonalAcc=" + s.BankAccount + "|BankName=" + s.BankName +
		"|BIC=" + s.BankBIK + "|CorrespAcc=" + s.BankCorrAccount + fmt.Sprintf("|Sum=%d", doc.Amount) +
		"|Purpose=" + purpose + "|PayeeINN=" + s.INN
	png, err := qrcode.Encode(payload, qrcode.Medium, 512)
	if err != nil {
		return nil, fmt.Errorf("render invoice payment QR: %w", err)
	}
	return png, nil
}
