package ord_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/logx"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type v8SchemaRegexp struct{ *regexp2.Regexp }

func (r v8SchemaRegexp) MatchString(value string) bool {
	matched, err := r.Regexp.MatchString(value)
	return err == nil && matched
}

func v8SchemaCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	body, err := os.ReadFile("../../../.ai-factory/references/yandex-ord-sandbox-openapi-2026-11-06.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		r, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		r.MatchTimeout = time.Second
		return v8SchemaRegexp{r}, nil
	})
	c.AssertFormat()
	if err := c.AddResource("https://fixture.invalid/ord.json", doc); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReportingV8WireBodies(t *testing.T) {
	compiler := v8SchemaCompiler(t)
	ctx := context.Background()
	for _, tc := range []struct {
		file, schema, path, key string
		call                    func(*ord.Client) (ord.Receipt, error)
	}{
		{"statistics_request.json", "Statistics", "statistics", "local-statistics-correlation", func(c *ord.Client) (ord.Receipt, error) {
			return c.SubmitStatisticsV8(ctx, "local-statistics-correlation", decode[ord.StatisticsV8](t, "v8/statistics_request.json"))
		}},
		{"invoice_request.json", "Invoice", "invoice", "fixture-publisher-invoice", func(c *ord.Client) (ord.Receipt, error) {
			return c.SubmitInvoiceV8(ctx, decode[ord.InvoiceV8](t, "v8/invoice_request.json"))
		}},
		{"agent_report_request.json", "Invoice", "invoice", "fixture-agent-report", func(c *ord.Client) (ord.Receipt, error) {
			return c.SubmitInvoiceV8(ctx, decode[ord.InvoiceV8](t, "v8/agent_report_request.json"))
		}},
		{"invoice_creatives_request.json", "InvoiceCreatives", "invoice/creatives", "local-links-correlation", func(c *ord.Client) (ord.Receipt, error) {
			return c.SubmitInvoiceCreativesV8(ctx, "local-links-correlation", decode[ord.InvoiceCreativesV8](t, "v8/invoice_creatives_request.json"))
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			calls := 0
			c, db := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v8/"+tc.path || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("wrong request routing/auth")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				sameJSON(t, body, testdata(t, "v8/"+tc.file))
				if bytes.Contains(body, []byte("local-")) {
					t.Error("local correlation key leaked into wire body")
				}
				schema, err := compiler.Compile("https://fixture.invalid/ord.json#/components/schemas/" + tc.schema)
				if err != nil {
					t.Error(err)
					return
				}
				value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
				if err != nil {
					t.Error(err)
					return
				}
				if err := schema.Validate(value); err != nil {
					t.Error(err)
				}
				_, _ = w.Write(testdata(t, "response_accepted.json"))
			})
			rec, err := tc.call(c)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || rec.RequestID == "" {
				t.Fatal("submission must return its own receipt after exactly one POST")
			}
			var key string
			if err := db.Pool.QueryRow(ctx, `SELECT object_id FROM outbound_request LIMIT 1`).Scan(&key); err != nil {
				t.Fatal(err)
			}
			if key != tc.key {
				t.Fatalf("journal key=%s want=%s", key, tc.key)
			}
		})
	}
}

func TestReportingV8PreservesZeroAndOmission(t *testing.T) {
	zero := int64(0)
	body, err := json.Marshal(ord.StatisticsV8{Statistics: []ord.StatisticV8{{CreativeID: "cr", PlatformID: "pl", DateStartFact: "2026-10-01", ImpsFact: &zero}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"impsFact":0`)) || bytes.Contains(body, []byte(`"impsPlan"`)) || bytes.Contains(body, []byte(`"amount"`)) {
		t.Fatalf("zero/unknown confused: %s", body)
	}
	for _, rate := range []string{"0", "100"} {
		amount := ord.TaxAmountV8{ExcludingVAT: "0.00", VAT: "0.00", IncludingVAT: "0.00", VATRate: rate}
		body, _ = json.Marshal(amount)
		var got ord.TaxAmountV8
		if err := json.Unmarshal(body, &got); err != nil || got != amount {
			t.Fatal("tax input changed")
		}
	}
	body, _ = json.Marshal(ord.InvoiceServicesV8{IntermediaryReportIDs: []string{"report"}})
	if bytes.Contains(body, []byte("includingVat")) || !bytes.Contains(body, []byte("intermediaryReportIdList")) {
		t.Fatalf("commission invoice includes invented totals: %s", body)
	}
}

func TestReportingV8EmptySubmissionRejectedLocally(t *testing.T) {
	// A zero client has no transport; these inputs must be rejected before I/O.
	c := &ord.Client{}
	ctx := context.Background()
	if _, err := c.SubmitStatisticsV8(ctx, "key", ord.StatisticsV8{}); err == nil {
		t.Fatal("empty statistics accepted")
	}
	if _, err := c.SubmitInvoiceV8(ctx, ord.InvoiceV8{}); err == nil {
		t.Fatal("empty invoice accepted")
	}
	if _, err := c.SubmitInvoiceCreativesV8(ctx, "key", ord.InvoiceCreativesV8{}); err == nil {
		t.Fatal("empty links accepted")
	}
}

func TestReportingV8DoesNotReplayUncertainWrites(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
			})
			_, err := c.SubmitInvoiceV8(context.Background(), decode[ord.InvoiceV8](t, "v8/invoice_request.json"))
			if err == nil || calls != 1 {
				t.Fatalf("error=%v POST count=%d; uncertain write replayed", err, calls)
			}
		})
	}
}

func TestSandboxReportingRejectsLegacyFieldsBeforeNetwork(t *testing.T) {
	c := ord.New(nil, nil, ord.Options{BaseURL: ord.TestBaseURL, APIVersion: "v8"}, logx.Discard())
	for _, tc := range []struct{ op, file string }{
		{ord.OpStatistics, "statistics_request.json"},
		{ord.OpInvoice, "invoice_request.json"},
		{ord.OpInvoiceCreatives, "invoice_creatives_request.json"},
	} {
		if _, err := c.SubmitSandbox(context.Background(), tc.op, "fixture", testdata(t, tc.file)); err == nil {
			t.Fatalf("legacy fields silently discarded for %s", tc.op)
		}
	}
}
