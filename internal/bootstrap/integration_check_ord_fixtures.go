package bootstrap

import (
	"encoding/json"
	"time"

	"github.com/leha-creator/chanly/internal/integration/ord"
)

// Contractual roles/documents are fictional and sandbox-only. Registry checks
// require existing corporate identities: use public Yandex examples/requisites,
// never private user records. These are not actual relationships between them.
// Sources: OpenAPI Organization.example, https://ord.yandex.ru/ and
// https://yandex.ru/legal/termsofuse_careerpro/ru/ (checked 2026-11-07).
func newORDSandboxRun(id string, now time.Time) *ordSandboxRun {
	r := &ordSandboxRun{Version: 1, RunID: id, Endpoint: ord.TestBaseURL, CreatedAt: now.UTC()}
	key := func(s string) string { return "check-" + id + "-" + s }
	add := func(name, operation, objectID string, body any) {
		payload, _ := json.Marshal(body) // fixtures consist solely of JSON-compatible values
		r.Steps = append(r.Steps, ordSandboxStep{Name: name, Operation: operation, ObjectID: objectID, Payload: payload, State: "prepared"})
	}
	adv, agent, publisher := key("advertiser"), key("agent"), key("publisher")
	initial, placement, platform, creative := key("initial"), key("placement"), key("platform"), key("creative")
	for i, party := range []struct {
		id, name, inn string
		rr            bool
	}{
		{adv, "ООО ЯНДЕКС", "7736207543", false},
		{agent, "ООО Яндекс Оператор Рекламных Данных", "9704149765", false},
		{publisher, "ООО Яндекс.Такси", "7704340310", true},
	} {
		add([]string{"advertiser", "agent", "publisher"}[i], ord.OpOrganization, party.id, map[string]any{
			"id": party.id, "type": "ul", "isOrs": false, "isRr": party.rr,
			"inn": party.inn, "name": party.name,
		})
	}
	// Use the preceding completed month, avoiding future dates in acts/statistics.
	monthEnd := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	monthStart := time.Date(monthEnd.Year(), monthEnd.Month(), 1, 0, 0, 0, 0, time.UTC)
	start, end := monthStart.Format(time.DateOnly), monthEnd.Format(time.DateOnly)
	add("initial-contract", ord.OpContract, initial, map[string]any{
		"id": initial, "type": "intermediary-contract", "clientId": adv, "contractorId": agent,
		"isRegReport": true, "date": start, "number": "TEST-AGENT-" + id,
		"subjectType": "mediation", "actionType": "conclude", "agentActingForPublisher": false,
	})
	add("publisher-contract", ord.OpContract, placement, map[string]any{
		"id": placement, "type": "contract", "clientId": agent, "contractorId": publisher,
		"isRegReport": false, "date": start, "number": "TEST-PUBLISHER-" + id,
		"subjectType": "distribution", "actionType": "distribution",
	})
	add("platform", ord.OpPlatforms, platform, map[string]any{
		"organizationId": publisher, "platforms": []any{map[string]any{
			"platformId": platform, "isOwned": true, "type": "site", "name": "Тестовая площадка MAX",
			"url": "https://max.ru/chanly_sandbox_check",
		}},
	})
	add("creative", ord.OpCreative, creative, map[string]any{
		"id": creative, "creativeType": "creative", "isSocial": false, "isSocialQuota": false,
		"coBranding": false, "contractIds": []string{initial}, "kktuCodes": []string{"30.15.1"},
		"description": "Тестовая услуга размещения рекламы для проверки интеграции",
		"form":        "text-block", "textData": []string{"Тестовый рекламный материал для проверки интеграции. Не предназначен для публикации."},
	})
	statAmount := sandboxAmount("900.00")
	add("statistics", ord.OpStatistics, key("statistics"), ord.StatisticsV8{Statistics: []ord.StatisticV8{{
		CreativeID: creative, PlatformID: platform, ImpsFact: sandboxPtr(int64(100)), ImpsPlan: sandboxPtr(int64(100)),
		DateStartFact: start, DateEndFact: end, DateStartPlan: start, DateEndPlan: end,
		Type: "other", AmountPerUnit: "900.00", Amount: &statAmount,
	}}})
	for _, invoice := range []struct{ name, contract, clientRole, contractorRole, kind, amount string }{
		{"publisher-invoice", placement, "psr", "rr", "invoice", "900.00"},
		{"agent-report", initial, "rd", "psr", "intermediary-report", "1000.00"},
	} {
		invoiceID := key(invoice.name)
		services := sandboxAmount(invoice.amount)
		amount := ord.InvoiceAmountV8{Services: ord.InvoiceServicesV8{TaxAmountV8: &services}}
		if invoice.kind == "intermediary-report" {
			commission := sandboxAmount("100.00")
			amount.Commission = &ord.InvoiceCommissionV8{TaxAmountV8: &commission, IsInvoice: sandboxPtr(false)}
		}
		add(invoice.name, ord.OpInvoice, invoiceID, ord.InvoiceV8{
			ID: invoiceID, ContractID: invoice.contract, ClientRole: invoice.clientRole,
			ContractorRole: invoice.contractorRole, Type: invoice.kind, Number: "TEST-" + invoice.name + "-" + id,
			Date: end, StartDate: start, EndDate: end, Amount: &amount,
			IsSocial: sandboxPtr(false), IsSocialQuota: sandboxPtr(false),
			Items: []ord.InvoiceAllocationV8{{ContractID: initial, Amount: services, IsSocial: sandboxPtr(false), IsSocialQuota: sandboxPtr(false)}},
		})
		add(invoice.name+"-creatives", ord.OpInvoiceCreatives, invoiceID, ord.InvoiceCreativesV8{
			Creatives: []ord.InvoiceCreativeLinksV8{{InvoiceID: invoiceID, ContractID: initial, CreativeIDs: []string{creative}}},
		})
	}
	return r
}

func sandboxAmount(amount string) ord.TaxAmountV8 {
	return ord.TaxAmountV8{ExcludingVAT: amount, IncludingVAT: amount, VAT: "0.00", VATRate: "100"}
}

func sandboxPtr[T any](value T) *T { return &value }
