package tbank

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestDecimal(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
		bad  bool
	}{{"0.01", 1, false}, {"12.34", 1234, false}, {"1e2", 10000, false}, {"92233720368547758.07", 9223372036854775807, false}, {"92233720368547758.08", 0, true}, {"0.001", 0, true}, {"-12.34", -1234, false}, {"1e10000", 0, true}, {"", 0, false}} {
		t.Run(tc.raw, func(t *testing.T) {
			v, err := decimal(json.Number(tc.raw))
			if (err != nil) != tc.bad {
				t.Fatalf("error %v", err)
			}
			if !tc.bad && tc.raw != "" && (v == nil || int64(*v) != tc.want) {
				t.Fatalf("amount %v", v)
			}
		})
	}
}

//go:embed testdata/statement.json
var fixture string

type transportFunc func(context.Context, outbound.Call) (outbound.Response, error)

func (f transportFunc) Do(c context.Context, r outbound.Call) (outbound.Response, error) {
	return f(c, r)
}

type noLimit struct{}

func (noLimit) Wait(context.Context, ratelimit.Bucket, ratelimit.Priority) error { return nil }
func query() domain.BankQuery {
	return domain.BankQuery{Account: "40802810838000000001", From: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
}
func TestClientContract(t *testing.T) {
	calls := 0
	out := transportFunc(func(_ context.Context, c outbound.Call) (outbound.Response, error) {
		calls++
		if !c.Sensitive || c.Method != "GET" || !strings.Contains(c.URL, "operationStatus=Transaction") || c.Header.Get("X-Request-Id") == "" {
			t.Fatalf("unsafe/invalid call %+v", c)
		}
		if calls == 1 {
			if !strings.Contains(c.URL, "withBalances=true") {
				t.Fatal("balances missing")
			}
			return outbound.Response{Body: []byte(fixture)}, nil
		}
		if strings.Contains(c.URL, "withBalances") || !strings.Contains(c.URL, "cursor=page2") {
			t.Fatal("invalid cursor query")
		}
		return outbound.Response{Body: []byte(`{"operations":[]}`)}, nil
	})
	c := New(out, noLimit{}, Options{BaseURL: SandboxURL, Token: "private", Account: query().Account}, logx.Discard())
	p, err := c.ReadPage(context.Background(), query())
	if err != nil || len(p.Incoming) != 1 || p.Incoming[0].Amount != 12345 || p.Incoming[0].IdentityConflict || p.Closing == nil {
		t.Fatalf("page %+v %v", p, err)
	}
	q := query()
	q.Cursor = p.NextCursor
	p, err = c.ReadPage(context.Background(), q)
	if err != nil || len(p.Incoming) != 0 || p.NextCursor != "" {
		t.Fatalf("empty page %+v %v", p, err)
	}
}
func TestInvalidOperationsDoNotCredit(t *testing.T) {
	for _, tc := range []struct{ from, to string }{{"Transaction", "Authorization"}, {"Transaction", "unknown"}, {"Credit", "Debit"}, {"643", "840"}, {"123.45", "0"}, {"40802810838000000001", "40802810838000000002"}, {`"inn":"7707083893"`, `"inn":null`}} {
		var r result
		if err := json.Unmarshal([]byte(strings.ReplaceAll(fixture, tc.from, tc.to)), &r); err != nil {
			t.Fatal(err)
		}
		p, err := normalize(r, query(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Incoming) != 0 {
			t.Fatalf("accepted %s", tc.to)
		}
	}
}
func TestIncompleteResponseAndAccessErrors(t *testing.T) {
	for _, body := range []string{`{}`, `{"operations":null}`, `<html>bad gateway</html>`} {
		c := New(transportFunc(func(context.Context, outbound.Call) (outbound.Response, error) {
			return outbound.Response{Body: []byte(body)}, nil
		}), noLimit{}, Options{Account: query().Account}, logx.Discard())
		if _, err := c.ReadPage(context.Background(), query()); err == nil {
			t.Fatal("incomplete page accepted")
		}
	}
	for _, status := range []int{401, 403, 429, 500} {
		sentinel := errors.New("safe provider failure")
		c := New(transportFunc(func(context.Context, outbound.Call) (outbound.Response, error) {
			return outbound.Response{Status: status}, sentinel
		}), noLimit{}, Options{Account: query().Account}, logx.Discard())
		if _, err := c.ReadPage(context.Background(), query()); !errors.Is(err, sentinel) {
			t.Fatal("provider failure lost")
		}
	}
}

func TestPayerWhitespaceDoesNotChangeIdentity(t *testing.T) {
	var r result
	raw := strings.ReplaceAll(fixture, `"inn":"7707083893"`, `"inn":" 7707083893 "`)
	raw = strings.ReplaceAll(raw, `"acct":"40702810500000000004"`, `"acct":" 40702810500000000004 "`)
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	p, err := normalize(r, query(), "")
	if err != nil || len(p.Incoming) != 1 || p.Incoming[0].PayerINN != "7707083893" || p.Incoming[0].PayerAccount != "40702810500000000004" {
		t.Fatalf("normalization %+v %v", p, err)
	}
}

func TestMoscowCalendarDocumentDate(t *testing.T) {
	for _, tc := range []struct {
		name, docDate, chargeDate string
		conflict                  bool
	}{
		{"Moscow midnight is a calendar date", "2026-10-18T21:00:00Z", "2026-10-19T10:00:00Z", false},
		{"other boundary timestamp stays quarantined", "2026-10-18T21:01:00Z", "2026-10-19T10:00:00Z", true},
		{"ambiguous charge date stays quarantined", "2026-10-18T21:00:00Z", "2026-10-18T21:00:00Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r result
			if err := json.Unmarshal([]byte(fixture), &r); err != nil {
				t.Fatal(err)
			}
			doc, _ := time.Parse(time.RFC3339, tc.docDate)
			charge, _ := time.Parse(time.RFC3339, tc.chargeDate)
			r.Operations[0].DocDate = &doc
			r.Operations[0].ChargeDate = &charge
			p, err := normalize(r, query(), "")
			if err != nil || len(p.Incoming) != 1 {
				t.Fatalf("normalize: %+v %v", p, err)
			}
			in := p.Incoming[0]
			if in.IdentityConflict != tc.conflict || !in.DocDate.Equal(doc) || in.DocDate.Location() != time.UTC {
				t.Fatalf("calendar handling: %+v", in)
			}
		})
	}
}

func TestObservedSandboxExampleIsNeverCredited(t *testing.T) {
	body, err := os.ReadFile("testdata/sandbox-static-anonymized.json")
	if err != nil {
		t.Fatal(err)
	}
	var r result
	if err = json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	q := query()
	for _, date := range []time.Time{q.From, time.Date(2022, 2, 2, 0, 0, 0, 0, time.UTC)} {
		q.From = date
		q.To = date.Add(24 * time.Hour)
		p, err := normalize(r, q, "")
		if err != nil || len(p.Incoming) != 0 || p.Rejected != 1 || p.NextCursor != "static-cursor" {
			t.Fatalf("sandbox example %+v %v", p, err)
		}
	}
}
