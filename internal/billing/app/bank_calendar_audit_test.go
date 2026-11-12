package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/tbank"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

type calendarTransport struct {
	t     *testing.T
	calls int
	read  func(url.Values) (map[string]any, error)
}

func (c *calendarTransport) Do(_ context.Context, call outbound.Call) (outbound.Response, error) {
	c.calls++
	u, err := url.Parse(call.URL)
	if err != nil {
		c.t.Fatal(err)
	}
	q := u.Query()
	for _, key := range []string{"from", "to"} {
		if !strings.HasSuffix(q.Get(key), "Z") {
			c.t.Fatalf("%s is not UTC: %s", key, q.Get(key))
		}
	}
	response, err := c.read(q)
	if err != nil {
		return outbound.Response{}, err
	}
	body, err := json.Marshal(response)
	return outbound.Response{Status: 200, Body: body}, err
}

type calendarLimiter struct{}

func (calendarLimiter) Wait(context.Context, ratelimit.Bucket, ratelimit.Priority) error { return nil }

func calendarTime(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func calendarOperation(inv domain.Invoice, docNumber string, paid, charged, documented time.Time) map[string]any {
	return map[string]any{
		"operationId": uuid.NewString(), "operationDate": paid.Format(time.RFC3339Nano),
		"chargeDate": charged.Format(time.RFC3339Nano), "docDate": documented.Format(time.RFC3339Nano),
		"operationStatus": "Transaction", "typeOfOperation": "Credit", "accountNumber": platformAccount,
		"accountCurrencyDigitalCode": "643", "accountAmount": json.Number("10.00"),
		"documentNumber": docNumber, "payPurpose": inv.PaymentDetails.Purpose,
		"payer":    map[string]string{"acct": "40702810500000000004", "inn": "7707083893", "name": "Test payer"},
		"receiver": map[string]string{"acct": platformAccount},
	}
}

// Simulate the bank's half-open UTC filter, independently of local calendar dates.
func calendarPage(t *testing.T, q url.Values, operations ...map[string]any) map[string]any {
	t.Helper()
	from := calendarTime(t, q.Get("from"))
	to := calendarTime(t, q.Get("to"))
	items := make([]map[string]any, 0, len(operations))
	for _, o := range operations {
		at := calendarTime(t, o["operationDate"].(string))
		if !at.Before(from) && at.Before(to) {
			items = append(items, o)
		}
	}
	return map[string]any{"operations": items}
}

func calendarSource(e *env, read func(url.Values) (map[string]any, error)) *calendarTransport {
	c := &calendarTransport{t: e.t, read: read}
	e.svc.BankSource = tbank.New(c, calendarLimiter{}, tbank.Options{Account: platformAccount, INN: e.svc.Config.Supplier.INN, BaseURL: tbank.SandboxURL}, logx.Discard())
	return c
}

func TestAutomaticBankCreditAcrossCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, paid, charged, documented, fileDay, documentDay string }{
		{"before Moscow midnight", "2026-11-11T20:59:59.999999999Z", "2026-11-11T20:59:59.999999999Z", "2026-11-10T21:00:00Z", "11.11.2026", "11.11.2026"},
		{"exact Moscow midnight", "2026-11-11T21:00:00Z", "2026-11-11T21:00:00Z", "2026-11-11T21:00:00Z", "12.11.2026", "12.11.2026"},
		{"reported night payment", "2026-11-11T22:45:57Z", "2026-11-11T22:45:57Z", "2026-11-11T21:00:00Z", "12.11.2026", "12.11.2026"},
		{"Astrakhan wire offset", "2026-11-12T02:45:57+04:00", "2026-11-12T02:45:57+04:00", "2026-11-12T01:00:00+04:00", "12.11.2026", "12.11.2026"},
		{"negative wire offset", "2026-11-11T17:45:57-05:00", "2026-11-11T17:45:57-05:00", "2026-11-11T16:00:00-05:00", "12.11.2026", "12.11.2026"},
		{"month boundary", "2026-11-30T21:00:00Z", "2026-11-30T21:00:00Z", "2026-11-30T21:00:00Z", "01.12.2026", "01.12.2026"},
		{"year boundary", "2026-12-31T21:00:00Z", "2026-12-31T21:00:00Z", "2026-12-31T21:00:00Z", "01.01.2027", "01.01.2027"},
		{"leap day", "2028-02-28T21:00:00Z", "2028-02-28T21:00:00Z", "2028-02-28T21:00:00Z", "29.02.2028", "29.02.2028"},
		{"after leap day", "2028-02-29T21:00:00Z", "2028-02-29T21:00:00Z", "2028-02-29T21:00:00Z", "01.03.2028", "01.03.2028"},
		{"weekend delayed posting", "2026-11-16T06:00:00Z", "2026-11-13T18:00:00Z", "2026-11-12T21:00:00Z", "13.11.2026", "13.11.2026"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			controlledBank(e)
			paid := calendarTime(t, tc.paid)
			e.clk.Set(paid.Add(-15 * time.Minute))
			inv := e.invoice(1000)
			e.svc.BankConfig.Start = paid.Add(-time.Hour)
			o := calendarOperation(inv, "802810", paid, calendarTime(t, tc.charged), calendarTime(t, tc.documented))
			transport := calendarSource(e, func(q url.Values) (map[string]any, error) { return calendarPage(t, q, o), nil })
			e.clk.Set(paid.Add(time.Second))
			first, err := e.svc.SyncBank(context.Background())
			if err != nil || first.Matched != 1 || first.Credited != 1000 {
				t.Fatalf("automatic credit: %+v %v", first, err)
			}
			// A restarted service uses the durable checkpoint and never credits twice.
			e.clk.Advance(5 * time.Minute)
			restarted := app.New(e.svc.Deps)
			if _, err := restarted.SyncBank(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Reimporting the same operation via a calendar-only 1C file is also safe.
			file := string(statementFile(platformAccount, "calendar audit", payment{802810, 1000, "7707083893", inv.PaymentDetails.Purpose}))
			file = strings.ReplaceAll(file, "19.10.2026", tc.fileDay)
			file = strings.ReplaceAll(file, "Дата="+tc.fileDay, "Дата="+tc.documentDay)
			if result := e.importStatement([]byte(file)); result.Duplicates != 1 {
				t.Fatalf("file duplicate: %+v", result)
			}
			progress, err := e.svc.BankSync.LoadBankProgress(context.Background(), platformAccount)
			if err != nil || !progress.LastManualCheck.IsZero() {
				t.Fatalf("manual check was needed: %+v %v", progress, err)
			}
			var stored time.Time
			// The original instant belongs to the API evidence record; the legacy
			// bank_transaction table intentionally has a date-only column.
			if err := e.d.Pool.QueryRow(context.Background(), "SELECT operation_date FROM bank_operation_observation WHERE operation_id=$1 LIMIT 1", o["operationId"]).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !stored.Equal(paid.Truncate(time.Microsecond)) {
				t.Fatalf("operation instant changed: %v != %v", stored, paid)
			}
			calls := transport.calls
			check, err := restarted.CheckInvoicePayment(context.Background(), e.org, inv.ID)
			if err != nil || check.Result != "paid" || transport.calls != calls {
				t.Fatalf("already paid check: %+v %v", check, err)
			}
			if e.balance(e.org) != 1000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
				t.Fatal("wrong balance or duplicate deposit")
			}
			e.converges()
		})
	}
}

func TestAutomaticBankCreditFindsDelayedOperationAfterYearBoundary(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	paid := calendarTime(t, "2026-12-31T20:59:50Z")
	e.clk.Set(paid.Add(-15 * time.Minute))
	inv := e.invoice(1000)
	e.svc.BankConfig.Start = paid.Add(-time.Hour)
	visible := false
	o := calendarOperation(inv, "101", paid, paid, calendarTime(t, "2026-12-30T21:00:00Z"))
	calendarSource(e, func(q url.Values) (map[string]any, error) {
		if visible {
			return calendarPage(t, q, o), nil
		}
		return calendarPage(t, q), nil
	})
	e.clk.Set(calendarTime(t, "2026-12-31T20:59:59Z"))
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 0 {
		t.Fatal("operation not yet reported by bank was credited")
	}
	visible = true
	e.clk.Set(calendarTime(t, "2027-01-01T00:01:00Z"))
	r, err := e.svc.SyncBank(context.Background())
	if err != nil || r.Credited != 1000 {
		t.Fatalf("overlap missed delayed operation: %+v %v", r, err)
	}
	if e.balance(e.org) != 1000 {
		t.Fatal("delayed operation not credited automatically")
	}
}

func TestAutomaticBankCreditResumesFailedPageAcrossMidnight(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	firstPaid := calendarTime(t, "2026-11-30T20:59:59Z")
	secondPaid := calendarTime(t, "2026-11-30T21:00:01Z")
	e.clk.Set(firstPaid.Add(-15 * time.Minute))
	one, two := e.invoice(1000), e.invoice(1000)
	e.svc.BankConfig.Start = firstPaid.Add(-time.Hour)
	a := calendarOperation(one, "101", firstPaid, firstPaid, calendarTime(t, "2026-11-29T21:00:00Z"))
	b := calendarOperation(two, "102", secondPaid, secondPaid, calendarTime(t, "2026-11-30T21:00:00Z"))
	fail := true
	var fixedTo string
	calendarSource(e, func(q url.Values) (map[string]any, error) {
		if q.Get("cursor") == "next" {
			if q.Get("to") != fixedTo {
				t.Fatal("resumed page changed its UTC window")
			}
			if fail {
				return nil, errors.New("temporary bank outage")
			}
			return calendarPage(t, q, b), nil
		}
		fixedTo = q.Get("to")
		page := calendarPage(t, q, a)
		page["nextCursor"] = "next"
		return page, nil
	})
	e.clk.Set(secondPaid.Add(time.Minute))
	if _, err := e.svc.SyncBank(context.Background()); err == nil {
		t.Fatal("page failure expected")
	}
	if e.balance(e.org) != 1000 {
		t.Fatal("first committed page lost or second page credited")
	}
	fail = false
	e.clk.Advance(24 * time.Hour)
	restarted := app.New(e.svc.Deps)
	if _, err := restarted.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 2000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 2 {
		t.Fatal("resume lost or duplicated a payment")
	}
	e.converges()
}

func TestAutomaticBankCreditAtExactCheckpointBoundary(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	paid := calendarTime(t, "2026-12-31T21:00:00Z")
	e.clk.Set(paid.Add(-15 * time.Minute))
	inv := e.invoice(1000)
	e.svc.BankConfig.Start = paid.Add(-time.Hour)
	e.svc.BankConfig.Overlap = 0
	o := calendarOperation(inv, "101", paid, paid, paid)
	calendarSource(e, func(q url.Values) (map[string]any, error) { return calendarPage(t, q, o), nil })
	e.clk.Set(paid)
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 0 {
		t.Fatal("exclusive upper boundary included")
	}
	e.clk.Advance(time.Second)
	if r, err := e.svc.SyncBank(context.Background()); err != nil || r.Credited != 1000 {
		t.Fatalf("next interval skipped exact boundary: %+v %v", r, err)
	}
	if e.balance(e.org) != 1000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
		t.Fatal("boundary credit not exactly once")
	}
}

func TestAutomaticBankCreditForExpiredInvoice(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	paid := calendarTime(t, "2027-01-01T00:00:01+03:00")
	e.clk.Set(paid.Add(-20 * 24 * time.Hour))
	inv := e.invoice(1000)
	e.svc.BankConfig.Start = paid.Add(-time.Hour)
	o := calendarOperation(inv, "101", paid, paid, calendarTime(t, "2027-01-01T00:00:00+03:00"))
	calendarSource(e, func(q url.Values) (map[string]any, error) { return calendarPage(t, q, o), nil })
	e.clk.Set(paid.Add(time.Second))
	if n, err := e.svc.ExpireInvoices(context.Background()); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if r, err := e.svc.SyncBank(context.Background()); err != nil || r.Credited != 1000 {
		t.Fatalf("late payment: %+v %v", r, err)
	}
	if e.balance(e.org) != 1000 {
		t.Fatal("valid late payment was not credited automatically")
	}
}
