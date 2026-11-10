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
	executor["selfemployed_profiles"] = []any{}
	if e, err := c.LookupExecutor(context.Background(), r); err != nil || e.Status == rocketwork.ContractorActive {
		t.Fatalf("missing profile: %+v %v", e, err)
	}
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
