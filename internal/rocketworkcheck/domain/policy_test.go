package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPolicyAuthority(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	e := Executor{ID: 1, INN: "123456789012", Employment: "selfemployed"}
	valid := Policy{AccountID: "canonical", CompanyINN: "1234567890", Version: 1,
		FixedAmount: 1000, MaxCommission: 100, TotalBudget: 10000, DailyBudget: 5000,
		DailyOperations: 3, Executors: []Executor{e}, Actions: []Action{Create, Complete, SignAct, Pay},
		NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	if err := valid.Authorize(now, "canonical", e, 1000, Create); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Policy){
		"empty":          func(p *Policy) { *p = Policy{} },
		"revoked":        func(p *Policy) { p.Revoked = true },
		"killed":         func(p *Policy) { p.Killed = true },
		"expired":        func(p *Policy) { p.ExpiresAt = now },
		"future":         func(p *Policy) { p.NotBefore = now.Add(time.Minute) },
		"fee":            func(p *Policy) { p.MaxCommission = -1 },
		"budget":         func(p *Policy) { p.DailyBudget = 999 },
		"unknown action": func(p *Policy) { p.Actions = []Action{"withdraw"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			if p.Authorize(now, "canonical", e, 1000, Create) == nil {
				t.Fatal("authorized")
			}
		})
	}
	if valid.Authorize(now, "alias", e, 1000, Create) == nil || valid.Authorize(now, "canonical", e, 999, Create) == nil || valid.Authorize(now, "canonical", e, 1001, Create) == nil || valid.Authorize(now, "canonical", e, 1000, Cancel) == nil {
		t.Fatal("invalid request authorized")
	}
	e.ID++
	if valid.Authorize(now, "canonical", e, 1000, Create) == nil {
		t.Fatal("executor substitution authorized")
	}
}

func TestPolicyJSONRequiresExplicitCommission(t *testing.T) {
	var p Policy
	if err := json.Unmarshal([]byte(`{"FixedAmount":1000}`), &p); err == nil {
		t.Fatal("incomplete policy accepted")
	}
	full := Policy{Executors: []Executor{}, Actions: []Action{}}
	data, _ := json.Marshal(full)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	delete(fields, "MaxCommission")
	data, _ = json.Marshal(fields)
	if json.Unmarshal(data, &p) == nil {
		t.Fatal("missing fee treated as zero")
	}
	fields["MaxCommission"] = json.RawMessage("null")
	data, _ = json.Marshal(fields)
	if json.Unmarshal(data, &p) == nil {
		t.Fatal("null fee treated as zero")
	}
	fields["MaxCommission"] = json.RawMessage("0")
	data, _ = json.Marshal(fields)
	if err := json.Unmarshal(data, &p); err != nil || p.MaxCommission != 0 {
		t.Fatalf("explicit zero lost: %v", err)
	}
}
