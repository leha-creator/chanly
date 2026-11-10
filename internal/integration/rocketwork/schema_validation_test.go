package rocketwork_test

import (
	"encoding/json"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworktest"
)

func contractValidator(t *testing.T) func(string, any) error { return rocketworktest.Validator(t) }
func TestContractValidatorRejectsMalformedMoneyAndRequiredFields(t *testing.T) {
	validate := contractValidator(t)
	for _, tc := range []struct {
		schema string
		body   any
		valid  bool
	}{
		{"Meta", map[string]any{"current": 1, "last": 1, "total": 0}, true},
		{"Meta", map[string]any{"current": 1, "last": 1}, false},
		{"Task", map[string]any{"task": map[string]any{"amount_gross": "10.01", "commission": "0"}}, true},
		{"Task", map[string]any{"task": map[string]any{"amount_gross": 10.01}}, false},
		{"TaskCreateParams", map[string]any{"description": "synthetic", "title": "synthetic", "amount_gross": json.Number("10.01")}, true},
		{"TaskCreateParams", map[string]any{"amount_gross": "10.01"}, false},
	} {
		if err := validate("/components/schemas/"+tc.schema, tc.body); (err == nil) != tc.valid {
			t.Errorf("%s valid=%t: %v", tc.schema, tc.valid, err)
		}
	}
}
