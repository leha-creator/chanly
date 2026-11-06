package ord_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestGetOrganizationStatus(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status              int
		body                string
		wantMissing, wantOK bool
	}{
		{"authenticated absence", 404, `{"detail":"Not found organization by id"}`, true, false},
		{"generic 404", 404, `{"detail":"Not Found"}`, false, false},
		{"unauthorized", 401, `{"detail":"Not authenticated"}`, false, false},
		{"found", 200, `{"object_id":"check-org","status":"ERIR async success","data":{"name":"private-name"}}`, false, true},
		{"wrong identity", 200, `{"object_id":"other"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("object_id") != "check-org" {
					t.Error("invalid lookup")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.GetOrganizationStatus(context.Background(), "check-org")
			if errors.Is(err, ord.ErrOrganizationNotFound) != tc.wantMissing || (err == nil) != tc.wantOK {
				t.Fatalf("unexpected error: %v", err)
			}
			var stored string
			if err := d.Pool.QueryRow(context.Background(), `SELECT response_body::text FROM outbound_request LIMIT 1`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "private-name") {
				t.Fatal("organization data persisted without redaction")
			}
		})
	}
}

const token = "ord-oauth-secret"

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode[T any](t *testing.T, name string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(testdata(t, name), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func newClient(t *testing.T, h http.HandlerFunc) (*ord.Client, *dbtest.DB) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC))
	log := logx.Discard()
	noSleep := func(context.Context, time.Duration) error { return nil }
	out := outbound.New(d.Pool, clk, log, outbound.Options{System: outbound.SystemYandexORD, Sleep: noSleep})
	lim := ratelimit.New(d.Pool, clk, log, ratelimit.Options{Sleep: func(_ context.Context, w time.Duration) error {
		clk.Advance(w)
		return nil
	}})
	return ord.New(out, lim, ord.Options{BaseURL: srv.URL, APIVersion: "v8", Token: token}, log), d
}

// sameJSON сравнивает тела как значения JSON.
func sameJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got body is not JSON: %s", got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("body mismatch:\n got  %s\n want %s", got, want)
	}
}

// Legacy report fixtures predate the verified v8 schema. Keep this compatibility
// test until the production domain mapping and durable two-step workflow migrate.
// Actual v8 report schema/HTTP coverage lives in TestReportingV8WireBodies.
func TestLegacyUpsertBodiesMatchFixtures(t *testing.T) {
	type seen struct{ method, path, auth string }
	var last seen
	var body []byte
	response := "response_accepted.json"
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		last = seen{r.Method, r.URL.Path, r.Header.Get("Authorization")}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write(testdata(t, response))
	})
	ctx := context.Background()
	cases := []struct {
		path, sample string
		call         func() (ord.Receipt, error)
	}{
		{"/api/v8/organization", "organization_request.json", func() (ord.Receipt, error) {
			return c.UpsertOrganization(ctx, decode[ord.Organization](t, "organization_request.json"))
		}},
		{"/api/v8/contract", "contract_request.json", func() (ord.Receipt, error) {
			return c.UpsertContract(ctx, decode[ord.Contract](t, "contract_request.json"))
		}},
		{"/api/v8/platforms", "platforms_request.json", func() (ord.Receipt, error) {
			return c.UpsertPlatforms(ctx, decode[ord.Platforms](t, "platforms_request.json"))
		}},
		// Legacy reporting bodies; these are NOT API v8 compatibility evidence.
		{"/api/v8/statistics", "statistics_request.json", func() (ord.Receipt, error) {
			return c.UpsertStatistics(ctx, decode[ord.Statistics](t, "statistics_request.json"))
		}},
		{"/api/v8/invoice", "invoice_request.json", func() (ord.Receipt, error) {
			return c.UpsertInvoice(ctx, decode[ord.Invoice](t, "invoice_request.json"))
		}},
		{"/api/v8/invoice/creatives", "invoice_creatives_request.json", func() (ord.Receipt, error) {
			return c.UpsertInvoiceCreatives(ctx, decode[ord.InvoiceCreatives](t, "invoice_creatives_request.json"))
		}},
	}
	for _, tc := range cases {
		rec, err := tc.call()
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if last.method != http.MethodPost || last.path != tc.path || last.auth != "Bearer "+token {
			t.Fatalf("%s: request %+v", tc.path, last)
		}
		sameJSON(t, body, testdata(t, tc.sample))
		if rec.RequestID != "539b1621-386b-4d5f-856e-21678d16db88" || rec.Status != "Request accepted by ORD" {
			t.Fatalf("%s: receipt %+v", tc.path, rec)
		}
	}

	// Токен креатива — в object_statuses[].token.
	response = "response_creative.json"
	rec, err := c.UpsertCreative(ctx, decode[ord.Creative](t, "creative_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, body, testdata(t, "creative_request.json"))
	if rec.Token != "" || len(rec.ObjectStatuses) != 1 || rec.ObjectStatuses[0].Token != "BRBCaQT7cqRWN3uEegTZAahjid1wCWQ" {
		t.Fatalf("creative receipt %+v", rec)
	}
}

func TestGetStatusParsesErrors(t *testing.T) {
	var query string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write(testdata(t, "status_error.json"))
	})
	rec, err := c.GetStatus(context.Background(), "539b1621")
	if err != nil {
		t.Fatal(err)
	}
	if query != "/api/v8/status?reqid=539b1621" {
		t.Fatalf("query %s", query)
	}
	if rec.Status != "ERIR async error" || len(rec.Errors()) != 1 || rec.ObjectStatuses[0].Errors()[0] != "ИНН не найден в ЕГРЮЛ" {
		t.Fatalf("receipt %+v", rec)
	}
}

func TestValidationErrorIsPermanent(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write(testdata(t, "validation_422.json"))
	})
	_, err := c.UpsertOrganization(context.Background(), decode[ord.Organization](t, "organization_request.json"))
	var v *ord.ValidationError
	if !job.IsPermanent(err) || !errors.As(err, &v) {
		t.Fatalf("want permanent validation error, got %v", err)
	}
	if got := v.Error(); got != "ord validation failed: body.inn: string does not match regex; body.kktuCodes.0: field required" {
		t.Fatalf("error text %q", got)
	}
}

func TestRetriesServerErrorsAndThrottling(t *testing.T) {
	var n atomic.Int32
	c, d := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		switch n.Add(1) {
		case 1, 2:
			w.WriteHeader(http.StatusInternalServerError)
		case 3:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			_, _ = w.Write(testdata(t, "response_accepted.json"))
		}
	})
	if _, err := c.UpsertOrganization(context.Background(), decode[ord.Organization](t, "organization_request.json")); err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	var attempts int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbound_request WHERE system = 'yandex_ord'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 {
		t.Fatalf("%d attempts recorded, want 4", attempts)
	}

	// Долгая задержка 429 откладывает задание.
	c, _ = newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.GetStatus(context.Background(), "r")
	if after, ok := job.RetryAfter(err); !ok || after != 2*time.Minute {
		t.Fatalf("want job retry after 2m, got %v", err)
	}
}

func TestOutboundRecordIsMasked(t *testing.T) {
	c, d := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(testdata(t, "response_creative.json"))
	})
	ctx := context.Background()
	if _, err := c.UpsertOrganization(ctx, decode[ord.Organization](t, "organization_request.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpsertCreative(ctx, decode[ord.Creative](t, "creative_request.json")); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Pool.Query(ctx, `SELECT operation, request::text FROM outbound_request ORDER BY created_at, operation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stored := map[string]string{}
	for rows.Next() {
		var op, req string
		if err := rows.Scan(&op, &req); err != nil {
			t.Fatal(err)
		}
		stored[op] = req
	}
	org, cr := stored["ORD.organization"], stored["ORD.creative"]
	if !strings.Contains(org, `"inn": "77****7543"`) || strings.Contains(org, "7736207543") {
		t.Fatalf("organization request not masked: %s", org)
	}
	if strings.Contains(cr, "Скидки") || !strings.Contains(cr, `"length": 24`) {
		t.Fatalf("creative text stored: %s", cr)
	}
	for op, req := range stored {
		if strings.Contains(req, token) {
			t.Fatalf("%s: OAuth token stored", op)
		}
	}
}
