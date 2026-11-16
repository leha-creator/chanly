package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func actFixture() (AgreementTerms, Payout, ActData, ActTemplates, time.Time) {
	at := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	p := Payout{ID: uuid.New(), BloggerOrganizationID: uuid.New(), Amount: 123456, CreatedAt: at.Add(-time.Minute)}
	a := ActData{OrganizationID: p.BloggerOrganizationID, LegalType: SelfEmployed, TaxRegime: "npd", OfferAcceptedAt: at.Add(-72 * time.Hour), OrderNumber: 7, PublicationURL: "https://max.ru/channel/post", TopHours: 1, DurationHours: 24, PublishedAt: at.Add(-25 * time.Hour), RemovedAt: at.Add(-time.Hour)}
	terms := AgreementTerms{Amount: p.Amount, Currency: "RUB", ExecutorID: 7, EmploymentKind: "selfemployed", Title: "выплата", Description: "размещение рекламных материалов", TaxRetention: true, CommissionUnlimited: true}
	return terms, p, a, ActTemplates{SelfEmployed: "Акт СМЗ", Entrepreneur: "Акт ИП", EntrepreneurNPD: "Акт ИП на НПД"}, at
}

func TestActTemplatesFollowPayeeTaxRegime(t *testing.T) {
	for _, tc := range []struct {
		legal     LegalType
		tax, name string
		retain    bool
	}{
		{SelfEmployed, "npd", "Акт СМЗ", true}, {SoleProprietor, "usn", "Акт ИП", false}, {SoleProprietor, "npd", "Акт ИП на НПД", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, p, data, templates, at := actFixture()
			data.LegalType, data.TaxRegime = tc.legal, tc.tax
			terms, err := base.WithAct(p, data, templates, at)
			if err != nil {
				t.Fatal(err)
			}
			if terms.DocumentName != tc.name || terms.TaxRetention != tc.retain || !terms.ActRequired {
				t.Fatalf("wrong act policy: %+v", terms)
			}
			if terms.DocumentNumber != "CH-"+p.ID.String() || terms.DocumentDate != "2026-11-16" || terms.PeriodFrom != "2026-11-15" || terms.PeriodTo != "2026-11-16" {
				t.Fatalf("wrong actual dates: %+v", terms)
			}
			vars := terms.Variables
			if vars.ExecutorID != p.BloggerOrganizationID.String() || vars.OfferAcceptedOn != "2026-11-13" || len(vars.Table) != 1 || vars.Table[0].OrderNumber != "7" || vars.Table[0].Cost != "1234.56" || vars.Table[0].Format != "1/24" {
				t.Fatalf("wrong variables: %+v", vars)
			}
			_, before, err := terms.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			terms.Variables.Table[0].Publication += "-changed"
			_, after, err := terms.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatal("act table missing from immutable terms hash")
			}
		})
	}
}

func TestActRefusesUnconfirmedOrIncompleteFacts(t *testing.T) {
	for _, change := range []func(*ActData){
		func(d *ActData) { d.OfferAcceptedAt = time.Time{} },
		func(d *ActData) { d.OfferAcceptedAt = time.Now().AddDate(100, 0, 0) },
		func(d *ActData) { d.OrganizationID = uuid.New() },
		func(d *ActData) { d.RemovedAt = time.Time{} },
		func(d *ActData) { d.PublicationURL = "" },
		func(d *ActData) { d.PublicationURL = "http://max.ru/post" },
		func(d *ActData) { d.LegalType = SoleProprietor; d.TaxRegime = "" },
	} {
		base, p, data, templates, at := actFixture()
		change(&data)
		if _, err := base.WithAct(p, data, templates, at); err == nil {
			t.Fatal("incomplete act accepted")
		}
	}
	base, p, data, templates, at := actFixture()
	base.TaxRetention = false
	terms, err := base.WithAct(p, data, templates, at)
	if err != nil || terms.TaxRetention {
		t.Fatalf("explicit tax policy ignored: %v", err)
	}
}
