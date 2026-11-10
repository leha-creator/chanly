package bridge

import (
	"context"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/payout/domain"
)

func (p PayoutProvider) ResolveExecutor(ctx context.Context, r domain.ExecutorLookup) (domain.ExecutorResolution, error) {
	api, ok := p.API.(interface {
		LookupExecutor(context.Context, rocketwork.ExecutorIdentity) (rocketwork.Executor, error)
	})
	if !ok {
		return domain.ExecutorResolution{}, domain.ErrProviderDisabled
	}
	e, err := api.LookupExecutor(ctx, rocketwork.ExecutorIdentity{Kind: r.LookupKind, Value: r.LookupValue, ExpectedID: r.ExpectedID, INN: r.INN, Phone: r.Phone, ExternalID: r.OrganizationID.String(), EmploymentKind: r.EmploymentKind})
	if err != nil {
		return domain.ExecutorResolution{}, providerErr(err)
	}
	return domain.ExecutorResolution{ExecutorID: e.ID, Status: domain.ContractorStatus(e.Status), Reason: e.Reason, NextAction: e.NextAction}, nil
}

func (p PayoutProvider) ExecuteOnboarding(ctx context.Context, action string, i domain.OnboardingInput) (string, error) {
	api, ok := p.API.(interface {
		InviteExecutor(context.Context, rocketwork.InvitationRequest) (string, error)
		RegisterEntrepreneur(context.Context, rocketwork.EntrepreneurRequest) (rocketwork.Executor, error)
		UpdateEntrepreneur(context.Context, rocketwork.EntrepreneurRequest, int64) (rocketwork.Executor, error)
		RebindExecutor(context.Context, rocketwork.ExecutorIdentity) (rocketwork.Executor, error)
	})
	if !ok {
		return "", domain.ErrProviderDisabled
	}
	var err error
	switch action {
	case "invite":
		result, err := api.InviteExecutor(ctx, rocketwork.InvitationRequest{Phone: i.Phone, WithFrameworkAgreement: i.WithFrameworkAgreement})
		return result, providerErr(err)
	case "register", "update":
		r := rocketwork.EntrepreneurRequest{INN: i.INN, Phone: i.Phone, FirstName: i.Names.FirstName, LastName: i.Names.LastName, SecondName: i.Names.SecondName}
		if action == "register" {
			_, err = api.RegisterEntrepreneur(ctx, r)
		} else {
			_, err = api.UpdateEntrepreneur(ctx, r, i.ExecutorID)
		}
	case "rebind":
		_, err = api.RebindExecutor(ctx, rocketwork.ExecutorIdentity{Kind: "inn", Value: i.INN, Phone: i.Phone, ExpectedID: i.ExecutorID, EmploymentKind: "selfemployed"})
	default:
		return "", domain.ErrInvalidPayout
	}
	return "response_received", providerErr(err)
}
