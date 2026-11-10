package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func syntheticTerms(form string) rocketwork.TaskTerms {
	t := rocketwork.TaskTerms{ExternalID: "synthetic-task", ExecutorINN: "000000000000", EmploymentKind: "entrepreneur", AmountKopecks: 123456789012345, Title: "Synthetic task", Description: "Synthetic placement service", SignatureForm: form, TaxRetention: false, ActRequired: true}
	if form == "manual" {
		t.DocumentNumber = "synthetic-contract"
		t.DocumentDate = "2026-11-09"
	} else {
		t.DocumentName = "synthetic-template"
		t.PeriodFrom = "2026-11-09"
		t.PeriodTo = "2026-12-09"
	}
	return t
}
func TestCreateTaskUsesDocumentedContractAndExactMoney(t *testing.T) {
	validate := contractValidator(t)
	for _, form := range []string{"online", "manual"} {
		t.Run(form, func(t *testing.T) {
			terms := syntheticTerms(form)
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/tasks" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("wrong creation request")
				}
				var body map[string]any
				decoder := json.NewDecoder(r.Body)
				decoder.UseNumber()
				if err := decoder.Decode(&body); err != nil {
					t.Fatal(err)
				}
				if err := validate("/components/schemas/TaskCreateParams", body); err != nil {
					t.Error(err)
				}
				if body["amount_gross"] != json.Number("1234567890123.45") || body["autopayable"] != false || body["autosignable"] != false || body["tax_retention"] != false || body["executor_inn"] != terms.ExecutorINN {
					t.Error("money or explicit policy changed")
				}
				if _, ok := body["signature_form"]; ok {
					t.Error("undocumented request property")
				}
				if form == "online" {
					if _, ok := body["document_number"]; ok {
						t.Error("online with manual agreement")
					}
				} else {
					if _, ok := body["document_name"]; ok {
						t.Error("manual with online template")
					}
				}
				executor := syntheticExecutor()
				executor["employment_kind"] = "entrepreneur"
				response := map[string]any{"task": map[string]any{"id": 7, "type": "OneTime", "external_id": terms.ExternalID, "status": "inwork", "amount_gross": "1234567890123.45", "executor": executor, "current_employment_kind": "entrepreneur"}}
				if err := validate("/components/schemas/Task", response); err != nil {
					t.Error(err)
				}
				writeJSON(w, 200, response)
			})
			got, err := c.CreateTask(context.Background(), terms)
			if err != nil || got.AmountKopecks != terms.AmountKopecks {
				t.Fatalf("create: %+v %v", got, err)
			}
		})
	}
}
func TestCreateTaskInvalidPolicyStopsBeforeNetwork(t *testing.T) {
	c := rocketwork.New(nil, nil, rocketwork.Options{}, logx.Discard())
	tm := syntheticTerms("manual")
	tm.DocumentDate = ""
	if _, err := c.CreateTask(context.Background(), tm); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal(err)
	}
	tm = syntheticTerms("online")
	tm.Autocomplete = true
	if _, err := c.CreateTask(context.Background(), tm); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal(err)
	}
}
