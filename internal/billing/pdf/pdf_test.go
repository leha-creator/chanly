package pdf_test

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/pdf"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestAmountInWords(t *testing.T) {
	for k, want := range map[money.Kopecks]string{
		1:          "Ноль рублей 01 копейка",
		100:        "Один рубль 00 копеек",
		2100:       "Двадцать один рубль 00 копеек",
		302:        "Три рубля 02 копейки",
		1112:       "Одиннадцать рублей 12 копеек",
		100000000:  "Один миллион рублей 00 копеек",
		123456789:  "Один миллион двести тридцать четыре тысячи пятьсот шестьдесят семь рублей 89 копеек",
		200100:     "Две тысячи один рубль 00 копеек",
		1100000:    "Одиннадцать тысяч рублей 00 копеек",
		5000000000: "Пятьдесят миллионов рублей 00 копеек",
		2122:       "Двадцать один рубль 22 копейки",
	} {
		if got := pdf.AmountInWords(k); got != want {
			t.Errorf("AmountInWords(%d) = %q, want %q", k, got, want)
		}
	}
	if got := pdf.FormatAmount(123456789); got != "1 234 567,89" {
		t.Errorf("FormatAmount = %q", got)
	}
	if got := pdf.FormatAmount(5); got != "0,05" {
		t.Errorf("FormatAmount = %q", got)
	}
}

var issued = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func renderer() pdf.Renderer {
	r := pdf.New(clock.NewManual(issued), logx.Discard())
	r.NoCompression = true
	return r
}

var supplier = domain.Supplier{
	Name: "ИП Петров Пётр Петрович", INN: "772900000001", OGRNIP: "320774600000001", Address: "г. Москва, ул. Тверская, д. 1",
	BankAccount: "40802810938000000001", BankName: "ПАО СБЕРБАНК", BankBIK: "044525225", BankCorrAccount: "30101810400000000225",
}

// contains — текст в потоке страницы: шрифт UTF-8 пишет символы кодами UTF-16BE.
func contains(data []byte, s string) bool {
	var enc []byte
	for _, u := range utf16.Encode([]rune(s)) {
		enc = append(enc, byte(u>>8), byte(u))
	}
	return bytes.Contains(data, enc)
}

func invoiceDoc() domain.InvoiceDoc {
	return domain.InvoiceDoc{
		Number: "MX-000101", IssuedAt: issued, ExpiresAt: issued.AddDate(0, 0, 5), Amount: 1234567,
		Supplier: supplier, Payer: domain.Payer{Name: `ООО "РОМАШКА"`, INN: "7707083893"}, VATNote: "Без НДС",
	}
}

func TestInvoice(t *testing.T) {
	r := renderer()
	data, err := r.Invoice(invoiceDoc())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Fatal("not a pdf")
	}
	for _, s := range []string{"MX-000101", "12 345,67", "Двенадцать тысяч триста сорок пять рублей 67 копеек", "Без НДС", "25.10.2026"} {
		if !contains(data, s) {
			t.Errorf("invoice has no %q", s)
		}
	}
	// Без банковских реквизитов плательщика — только наименование и ИНН (D-26).
	if contains(data, "р/с") {
		t.Error("payer bank details printed without them")
	}
	doc := invoiceDoc()
	doc.Payer.BankAccount, doc.Payer.BankBIK, doc.Payer.BankName = "40702810400000012345", "044525225", "ПАО СБЕРБАНК"
	withBank, err := r.Invoice(doc)
	if err != nil || !contains(withBank, "40702810400000012345") {
		t.Fatalf("payer bank details: %v", err)
	}
	// Метаданные без персональных данных.
	if contains(data, "РОМАШКА\x00") || bytes.Contains(data, []byte("7707083893)")) {
		t.Error("metadata")
	}
}

func TestLongNameWraps(t *testing.T) {
	doc := invoiceDoc()
	doc.Payer.Name = strings.Repeat("Общество с ограниченной ответственностью «Очень длинное наименование» ", 6)
	short, err := renderer().Invoice(invoiceDoc())
	if err != nil {
		t.Fatal(err)
	}
	long, err := renderer().Invoice(doc)
	if err != nil {
		t.Fatal(err)
	}
	// Перенос — несколько строк текста вместо одной обрезанной: строк вывода больше.
	if strings.Count(string(long), ")Tj") <= strings.Count(string(short), ")Tj")+3 {
		t.Fatal("long payer name is not wrapped")
	}
}

func TestDeterministic(t *testing.T) {
	a, err := pdf.New(clock.NewManual(issued), nil).Invoice(invoiceDoc())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pdf.New(clock.NewManual(issued), nil).Invoice(invoiceDoc())
	if !bytes.Equal(a, b) {
		t.Fatal("same data and clock gave different bytes")
	}
}

func TestClosingDocuments(t *testing.T) {
	period, _ := domain.ParsePeriod("2026-10")
	published, removed := issued, issued.Add(24*time.Hour)
	customer := domain.OrgRequisites{ID: uuid.New(), Name: `ООО "РОМАШКА"`, INN: "7707083893", KPP: "770701001"}
	report, err := renderer().AgentReport(domain.ReportDoc{
		Number: "А-000001", Period: period, IssuedAt: issued, Supplier: supplier, Principal: customer,
		Lines: []domain.ReportLine{
			{OrderNumber: 101, ChannelTitle: "Рецепты", PublishedAt: &published, RemovedAt: &removed, Payout: 750000, Fee: 112500},
			{OrderNumber: 102, ChannelTitle: "Сад", Payout: 0, Fee: 0, SettledAt: issued},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"А-000001", "октябрь 2026", "Рецепты", "7 500,00", "1 125,00", "20.10.2026", "21.10.2026"} {
		if !contains(report, s) {
			t.Errorf("report has no %q", s)
		}
	}
	act, err := renderer().Act(domain.ActDoc{Number: "А-000001", Period: period, IssuedAt: issued, Supplier: supplier,
		Customer: customer, FeeAmount: 112500, VATNote: "Без НДС"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"А-000001", "Одна тысяча сто двадцать пять рублей 00 копеек", "1 125,00"} {
		if !contains(act, s) {
			t.Errorf("act has no %q", s)
		}
	}
}
