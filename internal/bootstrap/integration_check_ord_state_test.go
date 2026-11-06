package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/clock"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type ordSchemaRegexp struct{ *regexp2.Regexp }

func (r ordSchemaRegexp) MatchString(value string) bool {
	matched, err := r.Regexp.MatchString(value)
	return err == nil && matched
}

func TestORDSandboxFixturesMatchOfficialSchema(t *testing.T) {
	body, err := os.ReadFile("../../.ai-factory/references/yandex-ord-sandbox-openapi-2026-11-06.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	// The official schema contains ECMAScript lookaheads unsupported by Go regexp.
	compiler.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		r, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		r.MatchTimeout = time.Second
		return ordSchemaRegexp{r}, nil
	})
	compiler.AssertFormat()
	const source = "https://fixture.invalid/ord.json"
	if err = compiler.AddResource(source, doc); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{ord.OpOrganization: "Organization", ord.OpContract: "Contract", ord.OpPlatforms: "Platforms", ord.OpCreative: "Creative", ord.OpStatistics: "Statistics", ord.OpInvoice: "Invoice", ord.OpInvoiceCreatives: "InvoiceCreatives"}
	r := newORDSandboxRun("0190a000-0000-7000-8000-000000000001", time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	for _, step := range r.Steps {
		t.Run(step.Name, func(t *testing.T) {
			schema, err := compiler.Compile(source + "#/components/schemas/" + names[step.Operation])
			if err != nil {
				t.Fatal(err)
			}
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(step.Payload))
			if err != nil {
				t.Fatal(err)
			}
			if err = schema.Validate(value); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type ordSandboxStub struct {
	posts, polls int
	submitErr    error
	status       string
}

func (s *ordSandboxStub) SubmitSandbox(_ context.Context, op, id string, _ json.RawMessage) (ord.Receipt, error) {
	s.posts++
	return ord.Receipt{RequestID: "receipt-" + id, Status: "Request accepted by ORD", Token: "test-erid"}, s.submitErr
}
func (s *ordSandboxStub) GetStatus(_ context.Context, id string) (ord.Receipt, error) {
	s.polls++
	return ord.Receipt{RequestID: id, Status: s.status, Token: "test-erid"}, nil
}

func sandboxTestRunner(t *testing.T, api *ordSandboxStub) (func() error, string) {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewManual(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	return func() error {
		return executeORDSandbox(context.Background(), api, clk, slog.New(slog.NewJSONHandler(io.Discard, nil)), dir)
	}, dir
}

func TestORDSandboxResumeWithoutDuplicateWrites(t *testing.T) {
	api := &ordSandboxStub{status: "ORD success"}
	run, dir := sandboxTestRunner(t, api)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if api.posts != 3 || api.polls != 3 {
		t.Fatal("intermediate status allowed next write or repost")
	}
	api.status = "ERIR async success"
	for i := 0; i < 12; i++ {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	if api.posts != 12 {
		t.Fatalf("posts=%d", api.posts)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if api.posts != 12 {
		t.Fatal("completed run repeated POST")
	}
	r, unlock, err := openORDSandboxRun(dir, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, s := range r.Steps {
		if s.State != "done" {
			t.Fatalf("unfinished %s", s.Name)
		}
	}
}

func TestORDSandboxUnknownOutcomeBlocksRetry(t *testing.T) {
	api := &ordSandboxStub{submitErr: errors.New("timeout after acceptance")}
	run, _ := sandboxTestRunner(t, api)
	if err := run(); err == nil {
		t.Fatal("uncertain result passed")
	}
	api.submitErr = nil
	if err := run(); err == nil {
		t.Fatal("uncertain result repeated")
	}
	if api.posts != 1 {
		t.Fatal("duplicate POST")
	}
}

func TestORDSandboxStateLockAndCorruption(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	_, unlock, err := openORDSandboxRun(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = openORDSandboxRun(dir, now); err == nil {
		t.Fatal("concurrent run accepted")
	}
	unlock()
	if err = os.WriteFile(filepath.Join(dir, "state.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = openORDSandboxRun(dir, now); err == nil {
		t.Fatal("broken state replaced")
	}
}

func TestORDSandboxRejectsForeignPayloadIDs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	r, unlock, err := openORDSandboxRun(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	// Contract reference changes while the step's own identity remains intact.
	r.Steps[3].Payload = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].Payload), r.Steps[0].ObjectID, "foreign-advertiser"))
	if err = saveORDSandboxRun(dir, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err = openORDSandboxRun(dir, now); err == nil {
		t.Fatal("foreign reference accepted")
	}
}

func TestORDSandboxDependencyGates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		done  []string
		ready bool
	}{
		{"agent", nil, true},
		{"publisher", nil, true},
		{"initial-contract", []string{"advertiser"}, false},
		{"initial-contract", []string{"advertiser", "agent"}, true},
		{"publisher-contract", []string{"advertiser", "agent"}, false},
		{"platform", []string{"publisher"}, true},
		{"statistics", []string{"creative"}, false},
		{"statistics", []string{"creative", "platform"}, true},
		{"publisher-invoice-creatives", []string{"creative"}, false},
		{"agent-report", []string{"initial-contract"}, false},
		{"unknown", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newORDSandboxRun("0190a000-0000-7000-8000-000000000001", time.Now())
			for i := range r.Steps {
				for _, name := range tc.done {
					if r.Steps[i].Name == name {
						r.Steps[i].State = "done"
					}
				}
			}
			if got := ordSandboxDependenciesDone(r, tc.name); got != tc.ready {
				t.Fatalf("ready=%t want %t", got, tc.ready)
			}
		})
	}
}

func TestORDSandboxReceiptRequiresAllObjects(t *testing.T) {
	s := ordSandboxStep{Operation: ord.OpCreative, State: "sent"}
	applyORDSandboxReceipt(&s, ord.Receipt{Status: "ERIR async success", Token: "erid", ObjectStatuses: []ord.ObjectStatus{{Status: "ORD success"}}})
	if s.State != "sent" {
		t.Fatal("premature success")
	}
	applyORDSandboxReceipt(&s, ord.Receipt{Status: "ERIR async success", ObjectStatuses: []ord.ObjectStatus{{Status: "ORD error", ErrorMessage: []json.RawMessage{json.RawMessage(`"GV_3 private data"`)}}}})
	if s.State != "failed" || len(s.ErrorCodes) != 1 || s.ErrorCodes[0] != "GV_3" {
		t.Fatal("object rejection not preserved safely")
	}
}

func TestORDSandboxOrganizationReadinessIsNotCompletion(t *testing.T) {
	r := newORDSandboxRun("0190a000-0000-7000-8000-000000000001", time.Now())
	for i := 0; i < 3; i++ {
		applyORDSandboxReceipt(&r.Steps[i], ord.Receipt{Status: "ERIR sync success"})
		if r.Steps[i].State != "sent" || !r.Steps[i].DependencyReady {
			t.Fatal("organization readiness must not mean final completion")
		}
	}
	if !ordSandboxDependenciesDone(r, "initial-contract") || !ordSandboxDependenciesDone(r, "platform") {
		t.Fatal("registered organizations unnecessarily block dependents")
	}
	applyORDSandboxReceipt(&r.Steps[0], ord.Receipt{Status: "ERIR sync success", ObjectStatuses: []ord.ObjectStatus{{Status: "ORD error"}}})
	if ordSandboxDependenciesDone(r, "initial-contract") || r.Steps[0].DependencyReady {
		t.Fatal("child failure must block dependents")
	}
	applyORDSandboxReceipt(&r.Steps[3], ord.Receipt{Status: "ERIR sync success"})
	if !r.Steps[3].DependencyReady || !ordSandboxDependenciesDone(r, "creative") {
		t.Fatal("registered contract must allow a sandbox creative attempt")
	}
	applyORDSandboxReceipt(&r.Steps[5], ord.Receipt{Status: "ERIR sync success"})
	applyORDSandboxReceipt(&r.Steps[6], ord.Receipt{Status: "ERIR sync success", Token: "test-erid"})
	if r.Steps[6].DependencyReady || ordSandboxDependenciesDone(r, "statistics") {
		t.Fatal("creative token/sync success must not unlock reporting")
	}
}
