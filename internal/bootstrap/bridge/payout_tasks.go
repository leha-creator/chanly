package bridge

import (
	"context"
	"strconv"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type taskAPI interface {
	CreateTask(context.Context, rocketwork.TaskTerms) (rocketwork.TaskSnapshot, error)
	GetTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
}

func parseProviderTaskID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, domain.ErrInvalidPayout
	}
	return id, nil
}
func providerTask(t rocketwork.TaskSnapshot) domain.ProviderTask {
	r := domain.ProviderTask{ID: t.ID, ExternalID: t.ExternalID, Type: t.Type, Status: t.Status, EmploymentKind: t.EmploymentKind, ExecutorID: t.ExecutorID, ExecutorINN: t.ExecutorINN, Amount: money.Kopecks(t.AmountKopecks), TaxRetention: t.TaxRetention, ActRequired: t.ActRequired, ReceiptURL: t.ReceiptURL, SignatureForm: t.SignatureForm, CustomerSigned: t.CustomerSigned, ExecutorSigned: t.ExecutorSigned}
	if t.CommissionKopecks != nil {
		v := money.Kopecks(*t.CommissionKopecks)
		r.Commission = &v
	}
	if t.ExecutorAmountKopecks != nil {
		v := money.Kopecks(*t.ExecutorAmountKopecks)
		r.ExecutorAmount = &v
	}
	if t.RetainedTaxKopecks != nil {
		v := money.Kopecks(*t.RetainedTaxKopecks)
		r.RetainedTax = &v
	}
	r.Documents = domain.PayoutDocuments{ReceiptURL: t.ReceiptURL, ManualRequired: t.Documents.ManualRequired, ManualStatus: t.Documents.ManualStatus, ManualFormURL: t.Documents.ManualFormURL}
	r.Documents.AgreementURL, r.Documents.AgreementSigningURL = t.Documents.AgreementURL, t.Documents.AgreementSigningURL
	r.Documents.AcceptanceURL, r.Documents.AcceptanceSigningURL = t.Documents.AcceptanceURL, t.Documents.AcceptanceSigningURL
	for _, doc := range t.Documents.FNS {
		r.Documents.FNS = append(r.Documents.FNS, domain.ReceiptDocument{Type: doc.Type, URL: doc.URL, RefundURL: doc.RefundURL})
	}
	for _, doc := range t.Documents.Fiscal {
		r.Documents.Fiscal = append(r.Documents.Fiscal, domain.ReceiptDocument{Type: doc.Type, URL: doc.URL, RefundURL: doc.RefundURL})
	}
	return r
}
func (p PayoutProvider) CreateTask(ctx context.Context, o domain.TaskOrder) (domain.ProviderTask, error) {
	api, ok := p.API.(taskAPI)
	if !ok {
		return domain.ProviderTask{}, domain.ErrProviderDisabled
	}
	t := o.Terms
	r, err := api.CreateTask(ctx, rocketwork.TaskTerms{ExternalID: o.ExternalID, ExecutorINN: o.ExecutorINN, EmploymentKind: t.EmploymentKind, AmountKopecks: int64(t.Amount), Title: t.Title, Description: t.Description, SignatureForm: t.SignatureForm, DocumentName: t.DocumentName, DocumentNumber: t.DocumentNumber, DocumentDate: t.DocumentDate, PeriodFrom: t.PeriodFrom, PeriodTo: t.PeriodTo, ActRequired: t.ActRequired, TaxRetention: t.TaxRetention, Autosignable: t.Autosignable, Autocomplete: t.Autocomplete, Autopayable: t.Autopayable})
	return providerTask(r), providerErr(err)
}
func (p PayoutProvider) ReadTask(ctx context.Context, id int64) (domain.ProviderTask, error) {
	api, ok := p.API.(taskAPI)
	if !ok {
		return domain.ProviderTask{}, domain.ErrProviderDisabled
	}
	r, err := api.GetTask(ctx, id)
	return providerTask(r), providerErr(err)
}
func (p PayoutProvider) FindTask(ctx context.Context, external string) (domain.ProviderTask, bool, error) {
	if api, ok := p.API.(interface {
		FindTask(context.Context, string) (rocketwork.TaskSnapshot, bool, error)
	}); ok {
		t, found, err := api.FindTask(ctx, external)
		return providerTask(t), found, providerErr(err)
	}
	r, found, err := p.API.FindPayout(ctx, "rw-payout:"+external)
	if err != nil || !found {
		return domain.ProviderTask{}, false, providerErr(err)
	}
	id, err := parseProviderTaskID(r.ID)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	t, err := p.ReadTask(ctx, id)
	return t, err == nil, err
}

func (p PayoutProvider) ActOnTask(ctx context.Context, id int64, action string) (domain.ProviderTask, error) {
	api, ok := p.API.(interface {
		CompleteTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
		SignTaskAct(context.Context, int64) (rocketwork.TaskSnapshot, error)
		PayTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
		CancelTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
	})
	if !ok {
		return domain.ProviderTask{}, domain.ErrProviderDisabled
	}
	var t rocketwork.TaskSnapshot
	var err error
	switch action {
	case "complete":
		t, err = api.CompleteTask(ctx, id)
	case "sign_act":
		t, err = api.SignTaskAct(ctx, id)
	case "pay":
		t, err = api.PayTask(ctx, id)
	case "cancel":
		t, err = api.CancelTask(ctx, id)
	default:
		return domain.ProviderTask{}, domain.ErrInvalidPayout
	}
	return providerTask(t), providerErr(err)
}
