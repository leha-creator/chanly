package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestWithdrawalVerifiesCompanyAndUsesOwnBankContract(t *testing.T) {
	validate := contractValidator(t)
	terms := rocketwork.WithdrawalTerms{ExternalID: "withdrawal:synthetic", CompanyINN: "0000000000", BIC: "000000000", AccountNumber: "00000000000000000000", ExecutorID: 7, AmountKopecks: 100001, MaxCommissionKopecks: 100}
	writes := 0
	own := true
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/account":
			inn := terms.CompanyINN
			if !own {
				inn = "1111111111"
			}
			writeJSON(w, 200, map[string]any{"balance": 1000000, "company_inn": inn})
		case "/api/executors/0000000000":
			writeJSON(w, 200, map[string]any{"executor": map[string]any{"id": 7, "inn": terms.CompanyINN, "type": "Withdrawal", "employment_kind": "self"}})
		case "/api/tasks":
			if r.Method != "POST" {
				t.Error("wrong withdrawal method")
			}
			writes++
			var body map[string]any
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			_ = decoder.Decode(&body)
			if err := validate("/components/schemas/TaskCreateParams", body); err != nil {
				t.Error(err)
			}
			if body["type"] != "Withdrawal" || body["amount_gross"] != json.Number("1000.01") || body["executor_inn"] != terms.CompanyINN {
				t.Error("wrong own-funds transfer")
			}
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": 9, "type": "Withdrawal", "external_id": terms.ExternalID, "amount_gross": "1000.01", "status": "paying", "current_employment_kind": "self", "executor": map[string]any{"id": 7, "inn": terms.CompanyINN}}})
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
	})
	if got, err := c.CreateWithdrawal(context.Background(), terms); err != nil || got.Type != "Withdrawal" {
		t.Fatal(err)
	}
	own = false
	if _, err := c.CreateWithdrawal(context.Background(), terms); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("foreign company accepted")
	}
	if writes != 1 {
		t.Fatal("foreign withdrawal sent")
	}
}
