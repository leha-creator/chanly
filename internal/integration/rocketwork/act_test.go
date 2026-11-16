package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func configuredActTerms() rocketwork.TaskTerms {
	t := syntheticTerms("online")
	t.Category, t.DocumentName, t.DocumentNumber, t.DocumentDate = "выплата", "Акт СМЗ", "CH-synthetic-1", "2026-11-16"
	t.AmountKopecks, t.TaxRetention, t.EmploymentKind = 2650000, true, "selfemployed"
	t.Variables = &rocketwork.ActVariables{ExecutorID: "org-synthetic", OfferAcceptedOn: "2026-10-24", Table: []rocketwork.ActRow{
		{OrderNumber: "1", Publication: "https://max.ru/post/1", Format: "1/24", Period: "2026-11-01 — 2026-11-02", Cost: "7500.00"},
		{OrderNumber: "3", Publication: "https://max.ru/post/3", Format: "2/48", Period: "2026-11-03 — 2026-11-05", Cost: "10000.00"},
		{OrderNumber: "7", Publication: "https://max.ru/post/7", Format: "1/24", Period: "2026-11-05 — 2026-11-06", Cost: "9000.00"},
	}}
	return t
}

func TestLegacyTermsHashInputDoesNotAcquireActFields(t *testing.T) {
	// Legacy manifests hash SDK terms separately from the provider wire DTO.
	data, err := json.Marshal(syntheticTerms("online"))
	if err != nil {
		t.Fatal(err)
	}
	var terms map[string]any
	if err = json.Unmarshal(data, &terms); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Category", "Variables"} {
		if _, ok := terms[key]; ok {
			t.Fatalf("new field %s changed legacy hash input", key)
		}
	}
}

func TestConfiguredActWireMatchesProviderExample(t *testing.T) {
	terms := configuredActTerms()
	validate := contractValidator(t)
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if err := validate("/components/schemas/TaskCreateParams", body); err != nil {
			t.Error(err)
		}
		if body["document_name"] != "Акт СМЗ" || body["document_number"] != "CH-synthetic-1" || body["document_date"] != "2026-11-16" || body["tax_retention"] != true || body["amount_gross"] != json.Number("26500.00") || body["category"] != "выплата" {
			t.Errorf("wrong act wire fields")
		}
		vars := body["metadata"].(map[string]any)["task_variables"].(map[string]any)
		rows := vars["table"].([]any)
		if vars["id_ispolnitelya"] != "org-synthetic" || vars["data_akcepta"] != "2026-10-24" || len(rows) != 3 || rows[0].(map[string]any)["stoimost_uslug"] != "7500.00" {
			t.Error("act variables or money changed")
		}
		if body["autopayable"] != false || body["autosignable"] != false || body["autocomplete"] != false {
			t.Error("act payload bypassed the durable workflow")
		}
		executor := syntheticExecutor()
		executor["employment_kind"] = "selfemployed"
		writeJSON(w, 200, map[string]any{"task": map[string]any{"id": 7, "type": "OneTime", "external_id": terms.ExternalID, "status": "inwork", "amount_gross": "26500.00", "executor": executor, "current_employment_kind": "selfemployed"}})
	})
	if _, err := client.CreateTask(context.Background(), terms); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredActRejectsWrongTotalsAndMissingFacts(t *testing.T) {
	for _, change := range []func(*rocketwork.TaskTerms){
		func(t *rocketwork.TaskTerms) { t.AmountKopecks++ },
		func(t *rocketwork.TaskTerms) { t.DocumentNumber = "" },
		func(t *rocketwork.TaskTerms) { t.Variables.Table[0].Cost = "7500.001" },
		func(t *rocketwork.TaskTerms) { t.Variables.OfferAcceptedOn = "" },
		func(t *rocketwork.TaskTerms) { t.Variables.Table[0].Publication = "http://max.ru/post" },
	} {
		terms := configuredActTerms()
		change(&terms)
		if err := terms.Validate(); !errors.Is(err, rocketwork.ErrFormat) {
			t.Fatalf("invalid act accepted: %v", err)
		}
	}
}
