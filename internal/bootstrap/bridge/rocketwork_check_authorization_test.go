package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestControlledWireCapabilityRejectsSubstitution(t *testing.T) {
	expected := rocketwork.WriteRequest{Operation: "Task.pay", Method: "PATCH", Path: "/api/tasks/7/pay", Body: json.RawMessage("null")}
	for name, mutate := range map[string]func(*rocketwork.WriteRequest){
		"foreign task": func(w *rocketwork.WriteRequest) { w.Path = "/api/tasks/8/pay" },
		"withdrawal": func(w *rocketwork.WriteRequest) {
			w.Operation = "CreateWithdrawal"
			w.Method = "POST"
			w.Path = "/api/tasks"
			w.Body = json.RawMessage(`{"type":"Withdrawal"}`)
		},
		"onboarding":         func(w *rocketwork.WriteRequest) { w.Path = "/api/executors/invite"; w.Method = "POST" },
		"subscriptions":      func(w *rocketwork.WriteRequest) { w.Path = "/api/postback_subscriptions"; w.Method = "DELETE" },
		"unknown action":     func(w *rocketwork.WriteRequest) { w.Operation = "Task.refund" },
		"late amount change": func(w *rocketwork.WriteRequest) { w.Before = &rocketwork.TaskSnapshot{ID: 7, AmountKopecks: 1001} },
		"missing final GET":  func(w *rocketwork.WriteRequest) { w.Before = nil },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			a := &checkAuthorization{expected: expected, grant: func(string) error { calls++; return nil }, validateBefore: func(t *rocketwork.TaskSnapshot) bool { return t != nil && t.ID == 7 && t.AmountKopecks == 1000 }}
			w := expected
			w.Before = &rocketwork.TaskSnapshot{ID: 7, AmountKopecks: 1000}
			mutate(&w)
			if a.AuthorizeWrite(context.Background(), w) == nil || calls != 0 {
				t.Fatal("substitution reached durable intent")
			}
		})
	}
	calls := 0
	a := &checkAuthorization{expected: expected, grant: func(string) error { calls++; return nil }}
	if a.AuthorizeWrite(context.Background(), expected) != nil {
		t.Fatal("exact request denied")
	}
	if a.AuthorizeWrite(context.Background(), expected) == nil || calls != 1 {
		t.Fatal("capability reused")
	}
}
