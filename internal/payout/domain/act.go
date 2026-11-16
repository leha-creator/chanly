package domain

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ActTemplates names the three templates configured in the customer's RW account.
type ActTemplates struct{ SelfEmployed, Entrepreneur, EntrepreneurNPD string }

func (t ActTemplates) Validate() error {
	for _, name := range []string{t.SelfEmployed, t.Entrepreneur, t.EntrepreneurNPD} {
		if strings.TrimSpace(name) == "" || len(name) > 200 {
			return fmt.Errorf("%w: act template name", ErrInvalidPayout)
		}
	}
	if t.SelfEmployed == t.Entrepreneur || t.SelfEmployed == t.EntrepreneurNPD || t.Entrepreneur == t.EntrepreneurNPD {
		return fmt.Errorf("%w: act templates must be distinct", ErrInvalidPayout)
	}
	return nil
}

type ActRow struct {
	OrderNumber string `json:"nomer_zakaza"`
	Publication string `json:"kanal_max_ssylka_na_pub"`
	Format      string `json:"format"`
	Period      string `json:"data_i_srok_razmeshcheniya"`
	Cost        string `json:"stoimost_uslug"`
}

type ActVariables struct {
	ExecutorID      string   `json:"id_ispolnitelya"`
	OfferAcceptedOn string   `json:"data_akcepta"`
	Table           []ActRow `json:"table"`
}

// ActData is read from the owning modules; examples or current env dates are never substituted.
type ActData struct {
	OrganizationID          uuid.UUID
	LegalType               LegalType
	TaxRegime               string
	OfferAcceptedAt         time.Time
	OrderNumber             int64
	PublicationURL          string
	TopHours, DurationHours int
	PublishedAt, RemovedAt  time.Time
}

type ActSource interface {
	ForAct(context.Context, Payout) (ActData, error)
}

func (a ActVariables) Validate() error {
	if a.ExecutorID == "" || len(a.ExecutorID) > 200 || len(a.Table) == 0 || len(a.Table) > 1000 {
		return fmt.Errorf("%w: act variables", ErrInvalidPayout)
	}
	if _, err := time.Parse(time.DateOnly, a.OfferAcceptedOn); err != nil {
		return fmt.Errorf("%w: act acceptance date", ErrInvalidPayout)
	}
	for _, row := range a.Table {
		u, err := url.Parse(row.Publication)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || row.OrderNumber == "" || row.Format == "" || row.Period == "" || row.Cost == "" {
			return fmt.Errorf("%w: act row", ErrInvalidPayout)
		}
	}
	return nil
}

// WithAct freezes one accepted placement per payout, preserving the existing immediate payout model.
func (t AgreementTerms) WithAct(p Payout, data ActData, templates ActTemplates, at time.Time) (AgreementTerms, error) {
	if err := templates.Validate(); err != nil {
		return t, err
	}
	if p.ID == uuid.Nil || data.OrganizationID != p.BloggerOrganizationID || p.Amount <= 0 || data.OrderNumber <= 0 ||
		data.OfferAcceptedAt.IsZero() || data.OfferAcceptedAt.After(p.CreatedAt) || data.PublishedAt.IsZero() ||
		!data.RemovedAt.After(data.PublishedAt) || data.RemovedAt.After(at) || data.DurationHours <= 0 || data.TopHours < 0 {
		return t, fmt.Errorf("%w: act source incomplete", ErrInvalidPayout)
	}
	npd := data.LegalType == SelfEmployed || data.TaxRegime == "npd"
	switch data.LegalType {
	case SelfEmployed:
		t.DocumentName = templates.SelfEmployed
	case SoleProprietor:
		if data.TaxRegime == "" {
			return t, fmt.Errorf("%w: tax regime required for act", ErrInvalidPayout)
		}
		if npd {
			t.DocumentName = templates.EntrepreneurNPD
		} else {
			t.DocumentName = templates.Entrepreneur
		}
	default:
		return t, fmt.Errorf("%w: act payee", ErrInvalidPayout)
	}
	moscow := time.FixedZone("MSK", 3*60*60)
	t.SignatureForm, t.Category = "online", "выплата"
	t.DocumentNumber, t.DocumentDate = "CH-"+p.ID.String(), at.In(moscow).Format(time.DateOnly)
	t.PeriodFrom, t.PeriodTo = data.PublishedAt.In(moscow).Format(time.DateOnly), data.RemovedAt.In(moscow).Format(time.DateOnly)
	t.TaxRetention = t.TaxRetention && npd
	t.ActRequired = true
	t.Variables = &ActVariables{ExecutorID: data.OrganizationID.String(), OfferAcceptedOn: data.OfferAcceptedAt.In(moscow).Format(time.DateOnly),
		Table: []ActRow{{OrderNumber: fmt.Sprint(data.OrderNumber), Publication: data.PublicationURL,
			Format: fmt.Sprintf("%d/%d", data.TopHours, data.DurationHours),
			Period: data.PublishedAt.In(moscow).Format("02.01.2006 15:04") + " — " + data.RemovedAt.In(moscow).Format("02.01.2006 15:04"),
			Cost:   fmt.Sprintf("%d.%02d", p.Amount/100, p.Amount%100)}}}
	return t, t.Validate()
}
