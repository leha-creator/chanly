package rocketwork

import (
	"errors"
	"testing"
)

func TestTaskPostbackBearer(t *testing.T) {
	secret := []byte("test-postback-secret")
	header := map[string][]string{"authorization": {"Bearer " + string(secret)}}
	body := []byte(`{"id":19,"event":"task.paid","happened_at":"2026-11-09T00:00:00Z","data":{"id":7,"status":"paid"}}`)
	for i := 0; i < 2; i++ {
		ev, err := parseTaskPostback(secret, header, body)
		if err != nil || ev.ObjectID != "7" || ev.EventID != "19" || ev.Kind != EventPaymentStatus {
			t.Fatalf("%+v: %v", ev, err)
		}
	}
	if _, err := parseTaskPostback(nil, header, body); !errors.Is(err, ErrSignature) {
		t.Fatal(err)
	}
	if _, err := parseTaskPostback(secret, nil, body); !errors.Is(err, ErrSignature) {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"id":19,"event":"task.paid","happened_at":"bad","data":{"id":7}}`,
		`{"id":19,"event":"task.paid","happened_at":"2026-11-09T00:00:00Z","data":{"id":7,"task":{"id":8}}}`,
		`{"id":19,"event":"executors.activated","happened_at":"2026-11-09T00:00:00Z","data":{"id":7}}`,
		`{"event":"task.paid","happened_at":"2026-11-09T00:00:00Z","data":{"id":7}}`,
	} {
		if _, err := parseTaskPostback(secret, header, []byte(invalid)); !errors.Is(err, ErrFormat) {
			t.Fatalf("invalid postback: %v", err)
		}
	}
	ev, err := parseTaskPostback(secret, header, []byte(`{"id":18,"event":"task.inwork","happened_at":"2026-11-08T00:00:00Z","data":{"task":{"id":7}}}`))
	if err != nil || ev.ObjectID != "7" {
		t.Fatalf("out of order callback: %+v %v", ev, err)
	}
	// Events carry no state; duplicates/out-of-order callbacks can only request polling.
	for _, tc := range []struct{ event, data, kind string }{
		{"executors.activated", `{"inn":"000000000000","phone":"+70000000000"}`, "executor_refresh"},
		{"company_accounts.balance_changed", `{}`, "deposit_refresh"},
		{"task.acquiring_order.paid", `{"id":7}`, "unknown"},
	} {
		body := []byte(`{"id":21,"event":"` + tc.event + `","happened_at":"2026-11-09T00:00:00Z","data":` + tc.data + `}`)
		ev, err := parsePostbackSecrets([][]byte{[]byte("new-secret"), secret}, header, body)
		if err != nil || ev.Kind != tc.kind || ev.ObjectID != "" {
			t.Fatalf("event leaked lookup or failed: %v", err)
		}
	}
}
