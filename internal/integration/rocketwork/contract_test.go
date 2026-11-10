package rocketwork

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestDecimalMoney(t *testing.T) {
	for raw, want := range map[string]int64{"0": 0, "1234.56": 123456, "0.01": 1, "-12.34": -1234, "1e2": 10000, "92233720368547758.07": 9223372036854775807} {
		got, err := rublesToKopecks(raw)
		if err != nil || got != want {
			t.Errorf("%s = %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "null", "0.001", "92233720368547758.08", "1e999999999", "1/2", "NaN"} {
		if _, err := rublesToKopecks(raw); !errors.Is(err, ErrFormat) {
			t.Errorf("accepted %q: %v", raw, err)
		}
	}
}

func TestTaskStatusesFromSuppliedOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../roketwork_openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage
		}
	}
	var taskSchema struct {
		AllOf []struct {
			Properties map[string]struct{ Enum []string }
		}
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spec.Components.Schemas["TaskProperties"], &taskSchema); err != nil {
		t.Fatal(err)
	}
	var statuses []string
	for _, part := range taskSchema.AllOf {
		statuses = append(statuses, part.Properties["status"].Enum...)
	}
	if len(statuses) == 0 {
		t.Fatal("missing provider status enum")
	}
	commission := "10.01"
	amount := "100.00"
	for _, status := range append(statuses, "future-status") {
		p, err := (taskDTO{ID: 7, Type: "OneTime", Status: status, Commission: &commission, AmountGross: &amount}).payment()
		if err != nil {
			t.Fatal(err)
		}
		want := PaymentSent
		if status == "paid" {
			want = PaymentSucceeded
		}
		if status == "canceled" {
			want = PaymentFailed
		}
		if p.Status != want {
			t.Errorf("%s: %+v", status, p)
		}
	}
	for _, d := range []taskDTO{{ID: 7, Type: "OneTime", Status: "paid"}, {ID: 7, Type: "Withdrawal", Status: "paid", Commission: &commission}, {Type: "OneTime", Status: "paid", Commission: &commission}} {
		if _, err := d.payment(); !errors.Is(err, ErrFormat) {
			t.Errorf("invalid task accepted: %v", err)
		}
	}
}
