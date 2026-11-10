package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// ---------------------------------------------------------------- payout → Рокет Ворк

// RocketWorkAPI — методы адаптера Рокет Ворк: rocketwork.Client, rocketwork.Disabled,
// rocketworkfake.Fake.
type RocketWorkAPI interface {
	EnsureContractor(ctx context.Context, r rocketwork.ContractorRequest) (rocketwork.Contractor, error)
	GetContractor(ctx context.Context, id string) (rocketwork.Contractor, error)
	GetDepositBalance(ctx context.Context) (int64, error)
	CreatePayout(ctx context.Context, r rocketwork.PaymentRequest) (rocketwork.Payment, error)
	GetPayout(ctx context.Context, id string) (rocketwork.Payment, error)
	FindPayout(ctx context.Context, key string) (rocketwork.Payment, bool, error)
	ParseWebhook(header map[string][]string, body []byte) (rocketwork.Event, error)
}

// PayoutProvider — payout/domain.Provider поверх адаптера: типы и ошибки переносятся
// один в один (приведение статусов сделал адаптер, R-2).
type PayoutProvider struct {
	API RocketWorkAPI
	Log *slog.Logger
}

// unknown — WARN о незнакомом статусе сервиса (R-2): объект продолжают опрашивать.
func (p PayoutProvider) unknown(ctx context.Context, object, id, _ string) {
	if p.Log != nil {
		p.Log.WarnContext(ctx, "rocketwork unknown status", logx.KeyOp, "RocketWork.Status", "object", object,
			"external_id", id, "status", "unknown")
	}
}

var _ domain.Provider = PayoutProvider{}

// providerErr переводит ошибки адаптера в ошибки домена; прочие (job.Retry предохранителя,
// отмена context) — как есть.
func providerErr(err error) error {
	var rej *rocketwork.RejectedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rej):
		return &domain.RejectedError{Code: domain.FailureCode(rej.Code), Message: rej.Message}
	case errors.Is(err, rocketwork.ErrDisabled):
		return domain.ErrProviderDisabled
	case errors.Is(err, rocketwork.ErrNotFound):
		return domain.ErrProviderNotFound
	case errors.Is(err, rocketwork.ErrSignature):
		return domain.ErrWebhookAuth
	case errors.Is(err, rocketwork.ErrFormat):
		return fmt.Errorf("%w: %v", domain.ErrWebhookFormat, err)
	case errors.Is(err, rocketwork.ErrUnavailable):
		return fmt.Errorf("%w: %v", domain.ErrProviderUnavailable, err)
	}
	return err
}

func contractorOf(c rocketwork.Contractor) domain.ProviderContractor {
	return domain.ProviderContractor{ExternalID: c.ID, Status: domain.ContractorStatus(c.Status),
		BlockedReason: c.BlockedReason, OnboardingURL: c.OnboardingURL}
}

func payoutOf(p rocketwork.Payment) domain.ProviderPayout {
	return domain.ProviderPayout{ExternalID: p.ID, Status: domain.Status(p.Status), Fee: money.Kopecks(p.FeeKopecks),
		ReceiptURL: p.ReceiptURL, FailureCode: domain.FailureCode(p.FailureCode), FailureMessage: p.FailureMessage}
}

func legalForm(t domain.LegalType) string {
	if t == domain.SoleProprietor {
		return rocketwork.LegalFormIndividual
	}
	return rocketwork.LegalFormSelfEmployed
}

func (p PayoutProvider) EnsureContractor(ctx context.Context, r domain.ContractorRequest) (domain.ProviderContractor, error) {
	c, err := p.API.EnsureContractor(ctx, rocketwork.ContractorRequest{ExternalID: r.OrganizationID.String(),
		INN: r.INN, FullName: r.FullName, Phone: r.Phone, LegalForm: legalForm(r.LegalType)})
	if err != nil {
		return domain.ProviderContractor{}, providerErr(err)
	}
	return contractorOf(c), nil
}

func (p PayoutProvider) GetContractor(ctx context.Context, externalID string) (domain.ProviderContractor, error) {
	c, err := p.API.GetContractor(ctx, externalID)
	if err != nil {
		return domain.ProviderContractor{}, providerErr(err)
	}
	if !c.Known {
		p.unknown(ctx, "contractor", c.ID, c.RawStatus)
	}
	return contractorOf(c), nil
}

func (p PayoutProvider) GetDepositBalance(ctx context.Context) (money.Kopecks, error) {
	b, err := p.API.GetDepositBalance(ctx)
	return money.Kopecks(b), providerErr(err)
}

func (p PayoutProvider) CreatePayout(ctx context.Context, o domain.PayoutOrder) (domain.ProviderPayout, error) {
	r, err := p.API.CreatePayout(ctx, rocketwork.PaymentRequest{IdempotencyKey: o.IdempotencyKey,
		ExternalID: o.PayoutID.String(), ContractorID: o.ContractorExternal, AmountKopecks: int64(o.Amount), Purpose: o.Purpose})
	if err != nil {
		return domain.ProviderPayout{}, providerErr(err)
	}
	return payoutOf(r), nil
}

func (p PayoutProvider) GetPayout(ctx context.Context, externalID string) (domain.ProviderPayout, error) {
	r, err := p.API.GetPayout(ctx, externalID)
	if err != nil {
		return domain.ProviderPayout{}, providerErr(err)
	}
	if !r.Known {
		p.unknown(ctx, "payout", r.ID, r.RawStatus)
	}
	return payoutOf(r), nil
}

func (p PayoutProvider) FindPayout(ctx context.Context, key string) (domain.ProviderPayout, bool, error) {
	r, found, err := p.API.FindPayout(ctx, key)
	if err != nil || !found {
		return domain.ProviderPayout{}, false, providerErr(err)
	}
	return payoutOf(r), true, nil
}

func (p PayoutProvider) ParseWebhook(header map[string][]string, body []byte) (domain.WebhookEvent, error) {
	ev, err := p.API.ParseWebhook(header, body)
	if err != nil {
		return domain.WebhookEvent{}, providerErr(err)
	}
	return domain.WebhookEvent{Kind: domain.WebhookKind(ev.Kind), ExternalID: ev.ObjectID, EventID: ev.EventID}, nil
}

// ---------------------------------------------------------------- placement → payout

// PlacementPayouts — placement/domain.Payouts поверх модуля выплат: переход в accepted
// создаёт выплату в той же транзакции (R-4).
type PlacementPayouts struct{ Svc *payoutapp.Service }

var _ placementdomain.Payouts = PlacementPayouts{}

func (p PlacementPayouts) Create(ctx context.Context, r placementdomain.PayoutRequest) (uuid.UUID, error) {
	return p.Svc.Create(ctx, payoutapp.PayoutRequest{PlacementID: r.PlacementID, BloggerOrgID: r.BloggerOrgID, Amount: r.Amount})
}

// NoPayouts — заглушка до подключения модуля выплат (I-1): принятие размещения выплату не создаёт.
type NoPayouts struct{}

var _ placementdomain.Payouts = NoPayouts{}

func (NoPayouts) Create(context.Context, placementdomain.PayoutRequest) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// ---------------------------------------------------------------- payout → placement

// PayoutPlacements — payout/domain.Placements поверх модуля размещений.
type PayoutPlacements struct {
	Svc        *placementapp.Service
	Placements *placementpg.Placements
}

var _ domain.Placements = PayoutPlacements{}

func (p PayoutPlacements) MarkPaid(ctx context.Context, placementID, payoutID uuid.UUID) error {
	err := p.Svc.MarkPaid(ctx, placementID, payoutID)
	if errors.Is(err, placementdomain.ErrNotAccepted) {
		return fmt.Errorf("%w: %v", domain.ErrPlacementState, err)
	}
	return err
}

func (p PayoutPlacements) Info(ctx context.Context, placementID uuid.UUID) (domain.PlacementInfo, error) {
	pl, err := p.Placements.Get(ctx, placementID)
	if errors.Is(err, placementdomain.ErrNotFound) {
		return domain.PlacementInfo{}, fmt.Errorf("%w: placement %s", domain.ErrNotFound, placementID)
	}
	if err != nil {
		return domain.PlacementInfo{}, err
	}
	info := domain.PlacementInfo{ID: pl.ID, Status: string(pl.Status), BloggerOrgID: pl.BloggerOrgID}
	if pl.PayoutAmount != nil {
		info.PayoutAmount = *pl.PayoutAmount
	}
	return info, nil
}

func (p PayoutPlacements) Brief(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.PlacementBrief, error) {
	refs, err := p.Placements.Refs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.PlacementBrief, len(refs))
	for id, r := range refs {
		out[id] = domain.PlacementBrief{OrderNumber: r.OrderNumber, ChannelID: r.ChannelID, ChannelTitle: r.ChannelTitle}
	}
	return out, nil
}

// ---------------------------------------------------------------- payout → ledger

// PayoutLedger — payout/domain.Ledger поверх журнала проводок (06 §2.3).
type PayoutLedger struct{ Svc *ledgerapp.Service }

var _ domain.Ledger = PayoutLedger{}

func (l PayoutLedger) Payout(ctx context.Context, p domain.Payout) error {
	_, err := l.Svc.Post(ctx, ledgerdomain.NewPayout(p.ID, p.PlacementID, p.BloggerOrganizationID, p.Amount, p.ProviderFee))
	return err
}

func (l PayoutLedger) LegalPayout(ctx context.Context, p domain.Payout, adminID uuid.UUID, reason string) error {
	_, err := l.Svc.Post(ctx, ledgerdomain.NewLegalPayout(p.ID, p.PlacementID, p.BloggerOrganizationID, p.Amount, adminID, reason))
	return err
}

func (l PayoutLedger) DepositBalance(ctx context.Context) (money.Kopecks, error) {
	t, err := l.Svc.Totals(ctx)
	if err != nil {
		return 0, err
	}
	return t[ledgerdomain.AccountRocketworkDeposit], nil
}

// ---------------------------------------------------------------- payout → account

// PayoutOrganizations — payout/domain.Organizations поверх организаций.
type PayoutOrganizations struct{ Orgs *accountpg.Organizations }

var _ domain.Organizations = PayoutOrganizations{}

func (o PayoutOrganizations) ForPayout(ctx context.Context, orgID uuid.UUID) (domain.Payee, error) {
	d, err := o.Orgs.ForPayout(ctx, orgID)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return domain.Payee{}, fmt.Errorf("%w: organization %s", domain.ErrNotFound, orgID)
	}
	if err != nil {
		return domain.Payee{}, err
	}
	return domain.Payee{OrganizationID: d.ID, LegalType: domain.LegalType(d.LegalType), INN: d.INN,
		FullName: d.FullName, Phone: d.Phone}, nil
}

// PayableBalance — остаток blogger_payable (сводка реестра выплат администратора).
func (l PayoutLedger) PayableBalance(ctx context.Context) (money.Kopecks, error) {
	t, err := l.Svc.Totals(ctx)
	if err != nil {
		return 0, err
	}
	return t[ledgerdomain.AccountBloggerPayable], nil
}
