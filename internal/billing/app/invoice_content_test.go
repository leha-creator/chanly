package app

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/billing/domain"
)

func TestInvoiceContent(t *testing.T) {
	for _, provider := range []string{"local", "tbank"} {
		for _, controlled := range []bool{false, true} {
			name := provider
			if controlled {
				name += "/controlled"
			}
			t.Run(name, func(t *testing.T) {
				s := Service{Deps: Deps{Config: Config{InvoiceProvider: provider, VATNote: "Без НДС"}}}
				if controlled {
					s.BankConfig = BankConfig{Access: domain.BankControlled, DevRealEnabled: true, DevPaymentCode: strings.Repeat("A", 48)}
				}
				inv := domain.Invoice{Number: "MX-999999999999999", CreatedAt: time.Date(2026, 11, 17, 21, 30, 0, 0, time.UTC)}
				doc := s.invoiceDoc(inv)
				want := "Средства принципала на исполнение поручения по оферте Chanly. Счёт № " + inv.Number + " от 18.11.2026. Без НДС"
				if provider == "tbank" && !strings.HasPrefix(doc.Comment, "Основание: оферта рекламодателя https://chanly.ru/offer\n") {
					t.Fatalf("wrong bank comment: %+v", doc)
				}
				if controlled {
					want += ". " + s.BankConfig.PaymentMarker()
				}
				if doc.Description != want {
					t.Fatalf("table description differs from payment purpose: %+v", doc)
				}
				if doc.PaymentPurpose != want || utf8.RuneCountInString(want) > 210 {
					t.Fatalf("wrong or oversized payment purpose (%d): %q", utf8.RuneCountInString(want), doc.PaymentPurpose)
				}
				if provider == "tbank" && (!strings.Contains(doc.Comment, "Назначение платежа: "+want) || utf8.RuneCountInString(doc.Comment) > 1000) {
					t.Fatalf("bank comment lost full payment purpose: %q", doc.Comment)
				}
				matched := domain.Match(domain.Incoming{Purpose: want}, domain.Candidates{ByNumber: []domain.Invoice{{Number: inv.Number, Status: domain.InvoiceIssued}}})
				if !matched.Matched || matched.Method != domain.MatchByNumber {
					t.Fatal("new purpose cannot be matched to invoice")
				}
			})
		}
	}
}
