package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

// CheckProvider composes an isolated client for each immutable diagnostic run.
// Factory does not attach the production automation capability.
type CheckProvider struct {
	Factory        func(rocketwork.WriteAuthorizer) *rocketwork.Client
	Origin, Target string
}

func checkTerms(t domain.Terms) rocketwork.TaskTerms { return rocketwork.TaskTerms(t) }
func (p CheckProvider) ValidateManifest(m domain.Manifest) error {
	if m.Target != p.Target || m.Origin != p.Origin || m.Executor.ID <= 0 {
		return domain.ErrDenied
	}
	return checkTerms(m.Terms).Validate()
}
func manifest(r domain.Run) (domain.Manifest, error) {
	var m domain.Manifest
	if json.Unmarshal(r.Manifest, &m) != nil || domain.Digest(m) != r.ManifestHash || m.RunID != r.ID || m.AccountID != r.AccountID || m.Terms.AmountKopecks != int64(r.Nominal) || m.Terms.ExternalID != r.ExternalID {
		return m, domain.ErrDenied
	}
	return m, nil
}
func matches(t rocketwork.TaskSnapshot, m domain.Manifest) bool {
	return t.ID > 0 && t.Type == "OneTime" && t.ExternalID == m.Terms.ExternalID && t.ExecutorID == m.Executor.ID && t.ExecutorINN == m.Executor.INN && t.EmploymentKind == m.Executor.Employment && t.AmountKopecks == m.Terms.AmountKopecks && t.TaxRetention != nil && *t.TaxRetention == m.Terms.TaxRetention && t.ActRequired != nil && *t.ActRequired == m.Terms.ActRequired && t.SignatureForm == m.Terms.SignatureForm
}

func (p CheckProvider) Inspect(ctx context.Context, r domain.Run) (domain.Observation, error) {
	m, err := manifest(r)
	if err != nil {
		return domain.Observation{}, err
	}
	if err := p.ValidateManifest(m); err != nil {
		return domain.Observation{}, err
	}
	c := p.Factory(nil)
	account, err := c.Account(ctx)
	if err != nil || account.CompanyINN != m.CompanyINN {
		return domain.Observation{}, domain.ErrDenied
	}
	e, err := c.LookupExecutor(ctx, rocketwork.ExecutorIdentity{Kind: "inn", Value: m.Executor.INN, ExpectedID: m.Executor.ID, EmploymentKind: m.Executor.Employment})
	if err != nil {
		return domain.Observation{}, domain.ErrDenied
	}
	o := domain.Observation{Ready: e.ID == m.Executor.ID && e.INN == m.Executor.INN && e.EmploymentKind == m.Executor.Employment && e.Status == rocketwork.ContractorActive && account.BalanceKopecks >= int64(r.Nominal+r.FeeCeiling)}
	var t rocketwork.TaskSnapshot
	found := false
	if r.TaskID != nil {
		t, err = c.GetTask(ctx, *r.TaskID)
		found = err == nil
	} else {
		t, found, err = c.FindTask(ctx, r.ExternalID)
	}
	if err != nil {
		return o, err
	}
	if !found {
		return o, nil
	}
	if t.AmountKopecks != m.Terms.AmountKopecks {
		return o, domain.ErrFinancialMismatch
	}
	if t.Status != "paid" && t.CommissionKopecks != nil && *t.CommissionKopecks > int64(r.FeeCeiling) {
		return o, domain.ErrFinancialMismatch
	}
	if !matches(t, m) || (r.TaskID != nil && t.ID != *r.TaskID) {
		return o, domain.ErrDenied
	}
	o.TaskID = &t.ID
	o.CanceledNoCharge = t.Status == "canceled" && t.CommissionKopecks != nil && *t.CommissionKopecks == 0
	o.Status = t.Status
	if t.Status == "paid" {
		o.ProviderPaidAt = t.PaidAt
	}
	o.Nominal = t.AmountKopecks
	if t.Status == "paid" && t.CommissionKopecks != nil && t.ExecutorAmountKopecks != nil && (!m.Terms.TaxRetention || t.RetainedTaxKopecks != nil) {
		o.Fee = t.CommissionKopecks
	}
	o.CanComplete = t.Status == "inwork" && t.CustomerSigned != nil && *t.CustomerSigned && t.ExecutorSigned != nil && *t.ExecutorSigned
	o.CanSign = t.Status == "accepting" && m.Terms.ActRequired
	o.CanPay = t.Status == "completed"
	o.CanCancel = (t.Status == "draft" || t.Status == "inwork") && t.CustomerSigned != nil && !*t.CustomerSigned && t.ExecutorSigned != nil && !*t.ExecutorSigned
	return o, nil
}

type checkAuthorization struct {
	expected       rocketwork.WriteRequest
	grant          func(string) error
	used           bool
	validateBefore func(*rocketwork.TaskSnapshot) bool
}

func (a *checkAuthorization) AuthorizeWrite(ctx context.Context, w rocketwork.WriteRequest) error {
	if a.used || w.Operation != a.expected.Operation || w.Method != a.expected.Method || w.Path != a.expected.Path || !bytes.Equal(w.Body, a.expected.Body) {
		return domain.ErrDenied
	}
	if a.validateBefore != nil && !a.validateBefore(w.Before) {
		return domain.ErrDenied
	}
	a.used = true
	return a.grant(domain.Digest(w))
}

func (p CheckProvider) Write(ctx context.Context, r domain.Run, action domain.Action, grant func(string) error) error {
	m, err := manifest(r)
	if err != nil {
		return err
	}
	// Repeat authoritative readiness and ownership checks immediately before write.
	o, err := p.Inspect(ctx, r)
	if err != nil || !o.Ready {
		return domain.ErrDenied
	}
	expected := rocketwork.WriteRequest{Operation: "Task." + string(action), Method: http.MethodPatch, Body: json.RawMessage("null")}
	if action == domain.Create {
		if o.TaskID != nil {
			return domain.ErrDenied
		}
		expected.Operation = "CreateTask"
		expected.Method = http.MethodPost
		expected.Path = "/api/tasks"
		expected.Body, err = checkTerms(m.Terms).CreateWire()
		if err != nil {
			return err
		}
	} else {
		if r.TaskID == nil || o.TaskID == nil || *r.TaskID != *o.TaskID {
			return domain.ErrDenied
		}
		expected.Path = "/api/tasks/" + strconv.FormatInt(*r.TaskID, 10) + "/" + string(action)
		allowed := false
		switch action {
		case domain.Complete:
			allowed = o.CanComplete
		case domain.SignAct:
			allowed = o.CanSign
		case domain.Pay:
			allowed = o.CanPay
		case domain.Cancel:
			allowed = o.CanCancel
		}
		if !allowed {
			return domain.ErrDenied
		}
	}
	validate := func(t *rocketwork.TaskSnapshot) bool {
		if action == domain.Create {
			return t == nil
		}
		return t != nil && r.TaskID != nil && t.ID == *r.TaskID && matches(*t, m) && checkActionAllowed(*t, m, action)
	}
	c := p.Factory(&checkAuthorization{expected: expected, grant: grant, validateBefore: validate})
	switch action {
	case domain.Create:
		_, err = c.CreateTask(ctx, checkTerms(m.Terms))
	case domain.Complete:
		_, err = c.CompleteTask(ctx, *r.TaskID)
	case domain.SignAct:
		_, err = c.SignTaskAct(ctx, *r.TaskID)
	case domain.Pay:
		_, err = c.PayTask(ctx, *r.TaskID)
	case domain.Cancel:
		_, err = c.CancelTask(ctx, *r.TaskID)
	default:
		return domain.ErrDenied
	}
	var rejected *rocketwork.RejectedError
	if errors.As(err, &rejected) {
		return domain.ErrWriteRejected
	}
	return err
}

var _ domain.Provider = CheckProvider{}

func checkActionAllowed(t rocketwork.TaskSnapshot, m domain.Manifest, action domain.Action) bool {
	switch action {
	case domain.Complete:
		return t.Status == "inwork" && t.CustomerSigned != nil && *t.CustomerSigned && t.ExecutorSigned != nil && *t.ExecutorSigned
	case domain.SignAct:
		return t.Status == "accepting" && m.Terms.ActRequired
	case domain.Pay:
		return t.Status == "completed"
	case domain.Cancel:
		return (t.Status == "draft" || t.Status == "inwork") && t.CustomerSigned != nil && !*t.CustomerSigned && t.ExecutorSigned != nil && !*t.ExecutorSigned
	}
	return false
}
