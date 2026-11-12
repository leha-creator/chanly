package rocketwork_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func syntheticExecutor() map[string]any {
	return map[string]any{"id": 7, "type": "Individual", "phone": "+79000000000", "inn": "000000000000", "first_name": "Synthetic", "last_name": "Fixture", "is_resident": true, "document_kind": "russian", "employment_kind": "selfemployed", "payment_readiness": "all_is_well", "selfemployed_status": "validated", "selfemployed_profiles": []any{map[string]any{"provider": "rocketwork", "selfemployed_status": "validated"}}}
}

func TestExecutorLookupChecksIdentityAndReadiness(t *testing.T) {
	validate := contractValidator(t)
	executor := syntheticExecutor()
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/executors/000000000000" {
			t.Error("wrong executor lookup")
		}
		body := map[string]any{"executor": executor}
		if err := validate("/components/schemas/ExecutorWrap", body); err != nil {
			t.Errorf("executor response: %v", err)
		}
		writeJSON(w, 200, body)
	})
	r := rocketwork.ExecutorIdentity{Kind: "inn", Value: "000000000000", ExpectedID: 7, Phone: "+79000000000", EmploymentKind: "selfemployed"}
	if e, err := c.LookupExecutor(context.Background(), r); err != nil || e.Status != rocketwork.ContractorActive {
		t.Fatalf("ready: %+v %v", e, err)
	}
	executor["payment_readiness"] = "no_requisites"
	if e, err := c.LookupExecutor(context.Background(), r); err != nil || e.Status != rocketwork.ContractorBlocked || e.NextAction != "add_requisites" {
		t.Fatalf("requisites: %+v %v", e, err)
	}
	executor["payment_readiness"] = "all_is_well"
	executor["id"] = 8
	if _, err := c.LookupExecutor(context.Background(), r); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("changed numeric identity accepted")
	}
	executor["id"] = 7
	executor["phone"] = "+79000000001"
	if _, err := c.LookupExecutor(context.Background(), r); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("changed contact accepted")
	}
}

func TestReadyExecutorsDoNotRequireRocketworkProfile(t *testing.T) {
	validate := contractValidator(t)
	for _, tc := range []struct {
		name     string
		profiles any
	}{
		{"public_status_source", []any{map[string]any{"provider": "free_smz", "selfemployed_status": "validated"}}},
		{"bank_status_source", []any{map[string]any{"provider": "alfa", "selfemployed_status": "validated"}}},
		{"empty_profiles", []any{}},
		{"omitted_profiles", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := syntheticExecutor()
			if tc.profiles == nil {
				delete(executor, "selfemployed_profiles")
			} else {
				executor["selfemployed_profiles"] = tc.profiles
			}
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected provider write: %s", r.Method)
				}
				if r.URL.Path == "/api/executors" {
					writeJSON(w, 200, map[string]any{"executors": []any{executor}, "meta": map[string]int{"current": 1, "last": 1, "total": 1}})
					return
				}
				body := map[string]any{"executor": executor}
				if err := validate("/components/schemas/ExecutorWrap", body); err != nil {
					t.Errorf("executor response: %v", err)
				}
				writeJSON(w, 200, body)
			})
			r := rocketwork.ExecutorIdentity{Kind: "inn", Value: "000000000000", ExpectedID: 7, Phone: "+79000000000", EmploymentKind: "selfemployed"}
			e, err := c.LookupExecutor(context.Background(), r)
			if err != nil || e.Status != rocketwork.ContractorActive || !e.Known || e.Reason != "" || e.NextAction != "" {
				t.Fatalf("ready lookup: %+v %v", e, err)
			}
			e, found, err := c.FindExecutor(context.Background(), r)
			if err != nil || !found || e.Status != rocketwork.ContractorActive || !e.Known || e.Reason != "" || e.NextAction != "" {
				t.Fatalf("ready search: %+v found=%t %v", e, found, err)
			}
		})
	}
}

func TestExecutorSummaryStillRejectsUnreadyExecutors(t *testing.T) {
	for _, tc := range []struct {
		name, readiness, selfemployed, employment string
		status, reason, action                    string
		known                                     bool
	}{
		{"missing_permissions", "no_tax_payment", "validated", "selfemployed", rocketwork.ContractorBlocked, "need_permissions", "grant_fns_permissions", true},
		{"missing_requisites", "no_requisites", "validated", "selfemployed", rocketwork.ContractorBlocked, "no_requisites", "add_requisites", true},
		{"incomplete_registration", "no_payments", "validated", "selfemployed", rocketwork.ContractorPending, "incomplete_profile", "complete_registration", true},
		{"unknown_readiness", "new_status", "validated", "selfemployed", rocketwork.ContractorPending, "unknown_readiness", "reconcile", false},
		{"null_readiness", "", "validated", "selfemployed", rocketwork.ContractorPending, "unknown_readiness", "reconcile", false},
		{"unknown_selfemployment", "all_is_well", "unknown", "selfemployed", rocketwork.ContractorPending, "selfemployment_unverified", "grant_fns_permissions", false},
		{"pending_selfemployment", "all_is_well", "pending", "selfemployed", rocketwork.ContractorPending, "selfemployment_unverified", "grant_fns_permissions", false},
		{"lost_selfemployment", "all_is_well", "not_selfemployed", "selfemployed", rocketwork.ContractorPending, "selfemployment_unverified", "grant_fns_permissions", false},
		{"revoked_permissions", "all_is_well", "need_permissions", "selfemployed", rocketwork.ContractorPending, "selfemployment_unverified", "grant_fns_permissions", false},
		{"null_selfemployment", "all_is_well", "", "selfemployed", rocketwork.ContractorPending, "selfemployment_unverified", "grant_fns_permissions", false},
		{"employment_mismatch", "all_is_well", "validated", "entrepreneur", rocketwork.ContractorPending, "employment_mismatch", "reconcile", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := syntheticExecutor()
			executor["payment_readiness"], executor["selfemployed_status"], executor["employment_kind"] = tc.readiness, tc.selfemployed, tc.employment
			if tc.readiness == "" {
				executor["payment_readiness"] = nil
			}
			if tc.selfemployed == "" {
				executor["selfemployed_status"] = nil
			}
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, map[string]any{"executor": executor})
			})
			e, err := c.LookupExecutor(context.Background(), rocketwork.ExecutorIdentity{Kind: "inn", Value: "000000000000", ExpectedID: 7, EmploymentKind: "selfemployed"})
			if err != nil || e.Status != tc.status || e.Reason != tc.reason || e.NextAction != tc.action || e.Known != tc.known {
				t.Fatalf("unready executor: %+v %v", e, err)
			}
		})
	}
}

func TestExecutorSearchRejectsAmbiguousPages(t *testing.T) {
	count := 0
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		e := syntheticExecutor()
		e["id"] = count
		writeJSON(w, 200, map[string]any{"executors": []any{e}, "meta": map[string]int{"current": count, "last": 2, "total": 2}})
	})
	_, found, err := c.FindExecutor(context.Background(), rocketwork.ExecutorIdentity{Kind: "inn", Value: "000000000000", EmploymentKind: "selfemployed"})
	if found || !errors.Is(err, rocketwork.ErrFormat) || count != 2 {
		t.Fatalf("ambiguous search: %t %d %v", found, count, err)
	}
}

func TestNumericExecutorIDIsNotALookup(t *testing.T) {
	c := rocketwork.New(nil, nil, rocketwork.Options{}, logx.Discard())
	if _, err := c.LookupExecutor(context.Background(), rocketwork.ExecutorIdentity{Kind: "id", Value: "7", EmploymentKind: "entrepreneur"}); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal(err)
	}
}
