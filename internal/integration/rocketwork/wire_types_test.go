package rocketwork

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskMoneyNeverDefaultsToZero(t *testing.T) {
	for _, amount := range []string{"", `,"amount_gross":null`, `,"amount_gross":"0.001"`, `,"amount_gross":"92233720368547758.08"`, `,"amount_gross":123`, `,"amount_gross":"-1"`} {
		var d taskDTO
		err := json.Unmarshal([]byte(`{"id":7,"type":"OneTime","status":"paid","commission":"0"`+amount+`}`), &d)
		if err == nil {
			_, err = d.payment()
		}
		if err == nil {
			t.Errorf("accepted invalid amount %s", amount)
		}
	}
	var d taskDTO
	if err := json.Unmarshal([]byte(`{"id":7,"type":"OneTime","status":"paying","amount_gross":"0","future_field":true}`), &d); err != nil {
		t.Fatal(err)
	}
	if p, err := d.payment(); err != nil || p.AmountKopecks != 0 {
		t.Fatalf("explicit zero: %v", err)
	}
}

func TestPaginationRequiresConsistentMetadata(t *testing.T) {
	for _, p := range []*pageDTO{nil, {}, {Current: 2, Last: 2, Total: 1}, {Current: 1, Last: 0, Total: 1}, {Current: 1, Last: 1, Total: -1}} {
		if err := p.validate(1, 1); !errors.Is(err, ErrFormat) {
			t.Errorf("accepted %+v", p)
		}
	}
}
