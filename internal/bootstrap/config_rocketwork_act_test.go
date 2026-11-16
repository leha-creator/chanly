package bootstrap

import "testing"

func TestNamedActPolicyUsesActualPlacementDates(t *testing.T) {
	base := RocketWorkConfig{Mode: RocketWorkLive, AccessMode: "automation", Purpose: "business", ProductionApproved: true, WorkflowEnabled: true, Target: "production", BaseURL: "https://api.rocketwork.ru", Token: "synthetic-token", AccountKey: "synthetic-account", WebhookSecret: "synthetic-webhook-secret-at-least-32", ContractMode: "online", TaskTitle: "выплата", TaskDescription: "размещение рекламных материалов", ActTemplatesEnabled: true, ActTemplateSelfEmployed: "Акт СМЗ", ActTemplateEntrepreneur: "Акт ИП", ActTemplateEntrepreneurNPD: "Акт ИП на НПД", CommissionUnlimited: true, Rate: 5}
	yes := true
	base.ActRequired = &yes
	base.TaxRetention = &yes
	config := Config{Env: EnvProduction, RocketWork: base}
	var issues []string
	config.validateRocketWork(func(field, _ string) { issues = append(issues, field) })
	// Other durations are supplied by LoadConfig; inspect only the new policy gate here.
	for _, field := range issues {
		if field == "ROCKETWORK_ACT_TEMPLATES_ENABLED" || field == "ROCKETWORK_CONTRACT_MODE" {
			t.Fatalf("act policy rejected: %v", issues)
		}
	}
	payout := config.PayoutAppConfig()
	if payout.ActTemplates == nil || payout.AgreementTemplate == nil || payout.AgreementTemplate.PeriodFrom != "" || payout.AgreementTemplate.DocumentNumber != "" || !payout.AgreementTemplate.TaxRetention {
		t.Fatal("act policy copied sample dates or lost tax policy")
	}
	config.RocketWork.ActTemplateEntrepreneurNPD = ""
	issues = nil
	config.validateRocketWork(func(field, _ string) { issues = append(issues, field) })
	found := false
	for _, field := range issues {
		if field == "ROCKETWORK_ACT_TEMPLATES_ENABLED" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing third template accepted")
	}
}
