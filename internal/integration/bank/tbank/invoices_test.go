package tbank

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

type invoiceTransport func(context.Context, outbound.Call) (outbound.Response, error)

func (f invoiceTransport) Do(ctx context.Context, c outbound.Call) (outbound.Response, error) {
	return f(ctx, c)
}

type invoiceLimiter struct{}

func (invoiceLimiter) Wait(context.Context, ratelimit.Bucket, ratelimit.Priority) error { return nil }

type pdfTransport func(*http.Request) (*http.Response, error)

func (f pdfTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInvoiceSendContract(t *testing.T) {
	id := uuid.New()
	calls := 0
	c := InvoiceClient{BaseURL: ProductionURL, Token: "secret", Account: "40802810300010250129", Limiter: invoiceLimiter{}, Out: invoiceTransport(func(_ context.Context, call outbound.Call) (outbound.Response, error) {
		calls++
		if !call.NoRetry || !call.Create || !call.Sensitive || call.Method != "POST" || call.URL != ProductionURL+"/api/v1/invoice/send" {
			t.Fatalf("unsafe invoice call: %+v", call)
		}
		if call.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("authorization missing")
		}
		raw, _ := json.Marshal(call.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["invoiceNumber"] != "123" || body["customPaymentPurpose"] != "MX-000123. DEV-TBANK-TEST" || !strings.Contains(string(raw), `"price":10.01`) {
			t.Fatalf("wrong payload: %s", raw)
		}
		if body["contacts"] != nil || body["contactPhone"] != nil {
			t.Fatal("must not send customer messages")
		}
		return outbound.Response{Status: 200, Body: []byte(`{"invoiceId":"` + id.String() + `","pdfUrl":"https://business.tbank.ru/document?signed=secret"}`)}, nil
	})}
	doc := domain.InvoiceDoc{Number: "MX-000123", Amount: 1001, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour), Supplier: domain.Supplier{BankAccount: c.Account}, PaymentPurpose: "MX-000123. DEV-TBANK-TEST"}
	got, err := c.Send(context.Background(), doc)
	if err != nil || got.ID != id || calls != 1 {
		t.Fatalf("result %+v, %v, calls %d", got, err, calls)
	}
	c.Out = invoiceTransport(func(context.Context, outbound.Call) (outbound.Response, error) {
		calls++
		return outbound.Response{}, context.DeadlineExceeded
	})
	if _, err = c.Send(context.Background(), doc); err == nil || calls != 2 {
		t.Fatal("timeout must be returned without a repeat")
	}
}

func TestInvoiceZeroNumberRejectedBeforeRequest(t *testing.T) {
	c := InvoiceClient{Account: "40802810300010250129"}
	for _, number := range []string{"MX-000000", "MX-0", "MX-", "MX-00001x"} {
		_, err := c.Send(context.Background(), domain.InvoiceDoc{Number: number, Amount: 1000, Supplier: domain.Supplier{BankAccount: c.Account}})
		if err == nil {
			t.Fatalf("invalid number %q accepted", number)
		}
	}
}

func TestInvoiceDatesUseMoscowCalendar(t *testing.T) {
	for _, tc := range []struct{ issued, day, due string }{
		{"2026-12-31T20:59:59Z", "2026-12-31", "2027-01-01"},
		{"2026-12-31T21:00:00Z", "2027-01-01", "2027-01-02"},
		{"2028-02-28T21:00:00Z", "2028-02-29", "2028-03-01"},
	} {
		t.Run(tc.day, func(t *testing.T) {
			issued, err := time.Parse(time.RFC3339, tc.issued)
			if err != nil {
				t.Fatal(err)
			}
			c := InvoiceClient{BaseURL: SandboxURL, Account: "40802810300010250129", Limiter: invoiceLimiter{}, Out: invoiceTransport(func(_ context.Context, call outbound.Call) (outbound.Response, error) {
				body := call.Body.(map[string]any)
				if body["invoiceDate"] != tc.day || body["dueDate"] != tc.due {
					t.Fatalf("wrong invoice dates: %+v", body)
				}
				return outbound.Response{Status: 200, Body: []byte(`{"invoiceId":"` + uuid.NewString() + `","pdfUrl":"https://business.tbank.ru/document"}`)}, nil
			})}
			_, err = c.Send(context.Background(), domain.InvoiceDoc{Number: "MX-000001", Amount: 1000, IssuedAt: issued, ExpiresAt: issued.Add(24 * time.Hour), Supplier: domain.Supplier{BankAccount: c.Account}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvoicePDFDownloadBoundaries(t *testing.T) {
	calls := 0
	c := InvoiceClient{PDFHosts: []string{"business.tbank.ru"}, HTTP: &http.Client{Transport: pdfTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Fatal("API token leaked to document server")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("%PDF-1.7 bank")), Header: http.Header{}}, nil
	})}}
	for _, bad := range []string{"http://business.tbank.ru/pdf", "https://evil.test/pdf", "https://business.tbank.ru.evil.test/pdf", "https://user@business.tbank.ru/pdf", "https://business.tbank.ru:8443/pdf", "https://127.0.0.1/pdf"} {
		if _, err := c.Download(context.Background(), bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if calls != 0 {
		t.Fatal("unsafe URL was requested")
	}
	if _, err := c.Download(context.Background(), "https://business.tbank.ru/pdf"); err != nil || calls != 1 {
		t.Fatalf("valid PDF: %v", err)
	}
	c.HTTP.Transport = pdfTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.test/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := c.Download(context.Background(), "https://business.tbank.ru/pdf"); err == nil {
		t.Fatal("redirect accepted")
	}
	c.HTTP.Transport = pdfTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret URL") })
	if _, err := c.Download(context.Background(), "https://business.tbank.ru/pdf"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe download error")
	}
}

// Opt-in contract probe: fixed sandbox URL and public sandbox token only.
// It cannot read a live token or create an invoice in the production environment.
func TestInvoiceSandboxContract(t *testing.T) {
	if os.Getenv("TBANK_INVOICE_SANDBOX_TEST") != "1" {
		t.Skip("set TBANK_INVOICE_SANDBOX_TEST=1 to probe the bank sandbox")
	}
	hc, err := max.NewHTTPClient("../../../../deploy/certs/russian_trusted_root_ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	hc.Timeout = 30 * time.Second
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := InvoiceClient{BaseURL: SandboxURL, Token: "TBankSandboxToken", Account: "40802123456789012345", Limiter: invoiceLimiter{}, HTTP: hc,
		Out: invoiceTransport(func(ctx context.Context, call outbound.Call) (outbound.Response, error) {
			body, err := json.Marshal(call.Body)
			if err != nil {
				return outbound.Response{}, err
			}
			req, err := http.NewRequestWithContext(ctx, call.Method, call.URL, strings.NewReader(string(body)))
			if err != nil {
				return outbound.Response{}, err
			}
			req.Header = call.Header.Clone()
			req.Header.Set("Content-Type", "application/json")
			res, err := hc.Do(req)
			if err != nil {
				return outbound.Response{}, errors.New("sandbox connection failed")
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			t.Logf("sandbox invoice HTTP %d", res.StatusCode)
			return outbound.Response{Status: res.StatusCode, Body: raw}, err
		})}
	now := time.Now()
	doc, err := c.Send(context.Background(), domain.InvoiceDoc{Number: "MX-900001", Amount: 1001, IssuedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour), Supplier: domain.Supplier{BankAccount: c.Account}, PaymentPurpose: "Пополнение баланса по счёту № MX-900001. DEV-TBANK-SANDBOX", Payer: domain.Payer{Name: "Тестовый плательщик", INN: "730990470834"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID == uuid.Nil || !strings.HasPrefix(doc.PDFURL, "https://") {
		t.Fatal("sandbox returned an incomplete invoice document")
	}
}
