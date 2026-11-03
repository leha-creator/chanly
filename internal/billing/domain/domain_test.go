package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestFormatNumber(t *testing.T) {
	for seq, want := range map[int64]string{1: "MX-000001", 999999: "MX-999999", 1000000: "MX-1000000"} {
		if got := FormatNumber(seq); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", seq, got, want)
		}
	}
}

func TestFindNumbers(t *testing.T) {
	cases := map[string][]string{
		"Пополнение баланса по счёту № MX-000123":        {"MX-000123"},
		"оплата по счету №MX-000123 без НДС":             {"MX-000123"},
		"оплата по счету mx-000123":                      {"MX-000123"},
		"по счёту МХ 000045 (кириллица)":                 {"MX-000045"},
		"MX000007, MX-000007 и MX-1000001":               {"MX-000007", "MX-1000001"},
		"MX-12345 — пять цифр, не номер":                 nil,
		"Оплата по договору 15 от 01.10.2026, без счёта": nil,
	}
	for purpose, want := range cases {
		if got := FindNumbers(purpose); !reflect.DeepEqual(got, want) {
			t.Errorf("FindNumbers(%q) = %v, want %v", purpose, got, want)
		}
	}
}

func TestValidateAmount(t *testing.T) {
	if ValidateAmount(MinInvoiceAmount-1) == nil || ValidateAmount(MaxInvoiceAmount+1) == nil {
		t.Fatal("out of range accepted")
	}
	if ValidateAmount(MinInvoiceAmount) != nil || ValidateAmount(MaxInvoiceAmount) != nil {
		t.Fatal("bounds rejected")
	}
}

func TestInvoiceTransitions(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	issued := func() *Invoice {
		return &Invoice{Status: InvoiceIssued, Amount: 2000000, ExpiresAt: now.Add(14 * 24 * time.Hour)}
	}

	inv := issued()
	if err := inv.Expire(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expire before deadline: %v", err)
	}
	if err := inv.Expire(inv.ExpiresAt); err != nil || inv.Status != InvoiceExpired {
		t.Fatalf("expire at deadline: %v, %s", err, inv.Status)
	}
	// S-06: поступление по истёкшему счёту зачисляется.
	if err := inv.MarkPaid(1900000, now); err != nil || inv.Status != InvoicePaid {
		t.Fatalf("pay expired: %v", err)
	}
	if inv.Discrepancy() != -100000 {
		t.Fatalf("discrepancy = %d", inv.Discrepancy())
	}
	if err := inv.MarkPaid(1, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pay twice: %v", err)
	}
	if err := inv.Cancel("x", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancel paid: %v", err)
	}

	inv = issued()
	if err := inv.Cancel("", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancel without reason: %v", err)
	}
	if err := inv.Cancel("ошибка суммы", now); err != nil || inv.Status != InvoiceCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if err := inv.MarkPaid(1, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pay cancelled: %v", err)
	}
}

func TestMatch(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	inv := func(num string, org uuid.UUID, inn string, amount money.Kopecks, st InvoiceStatus) Invoice {
		return Invoice{Number: num, OrganizationID: org, Amount: amount, Status: st, Payer: Payer{INN: inn}}
	}
	a1 := inv("MX-000001", orgA, "7707083893", 2000000, InvoiceIssued)
	a2 := inv("MX-000002", orgA, "7707083893", 2000000, InvoiceIssued)
	aExpired := inv("MX-000003", orgA, "7707083893", 500000, InvoiceExpired)
	aCancelled := inv("MX-000004", orgA, "7707083893", 2000000, InvoiceCancelled)
	b1 := inv("MX-000005", orgB, "500100732259", 300000, InvoiceIssued)

	cases := []struct {
		name   string
		in     Incoming
		c      Candidates
		org    uuid.UUID
		method MatchMethod
	}{
		{"by number", Incoming{Purpose: "по счёту MX-000001", Amount: 100, PayerINN: "1"},
			Candidates{ByNumber: []Invoice{a1}}, orgA, MatchByNumber},
		{"expired by number", Incoming{Purpose: "MX-000003", Amount: 500000},
			Candidates{ByNumber: []Invoice{aExpired}}, orgA, MatchByNumber},
		{"cancelled number falls back to inn", Incoming{Purpose: "MX-000004", Amount: 300000, PayerINN: "500100732259"},
			Candidates{ByNumber: []Invoice{aCancelled}, ByINNAndAmount: []Invoice{b1}}, orgB, MatchByINNAmount},
		{"by inn and amount", Incoming{Purpose: "пополнение", Amount: 300000, PayerINN: "500100732259"},
			Candidates{ByINNAndAmount: []Invoice{b1}}, orgB, MatchByINNAmount},
		{"two candidates", Incoming{Purpose: "пополнение", Amount: 2000000, PayerINN: "7707083893"},
			Candidates{ByINNAndAmount: []Invoice{a1, a2}}, uuid.Nil, ""},
		{"amount differs", Incoming{Purpose: "пополнение", Amount: 1999999, PayerINN: "7707083893"},
			Candidates{ByINNAndAmount: []Invoice{a1}}, uuid.Nil, ""},
		{"no inn", Incoming{Purpose: "пополнение", Amount: 2000000},
			Candidates{ByINNAndAmount: []Invoice{a1}}, uuid.Nil, ""},
		{"expired is not matched by inn", Incoming{Purpose: "x", Amount: 500000, PayerINN: "7707083893"},
			Candidates{ByINNAndAmount: []Invoice{aExpired}}, uuid.Nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Match(c.in, c.c)
			if r.Matched != (c.org != uuid.Nil) || r.OrganizationID != c.org || r.Method != c.method {
				t.Fatalf("Match = %+v, want org %s method %q", r, c.org, c.method)
			}
		})
	}
}

func TestPeriod(t *testing.T) {
	// 31.01 22:30 UTC — уже 1 февраля по Москве.
	p := PeriodOf(time.Date(2026, 1, 31, 22, 30, 0, 0, time.UTC))
	if p.String() != "2026-02" {
		t.Fatalf("PeriodOf = %s, want 2026-02", p)
	}
	if !p.Start().Equal(time.Date(2026, 1, 31, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("Start = %s", p.Start())
	}
	if !p.End().Equal(time.Date(2026, 2, 28, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("End = %s", p.End())
	}
	if prev := PreviousPeriod(time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)); prev.String() != "2025-12" {
		t.Fatalf("PreviousPeriod = %s", prev)
	}
	q, err := ParsePeriod("2026-10")
	if err != nil || q.String() != "2026-10" || !q.Date().Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParsePeriod = %v, %v", q, err)
	}
	if PeriodFromDate(q.Date()) != q {
		t.Fatal("PeriodFromDate")
	}
	if _, err := ParsePeriod("10.2026"); err == nil {
		t.Fatal("bad period accepted")
	}
	if FormatActNumber(45) != "А-000045" {
		t.Fatal("FormatActNumber")
	}
}
