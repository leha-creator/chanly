package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/billing/domain"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
)

// ---------------------------------------------------------------- billing ← account

// BillingOrganizations — billing/domain.Organizations поверх хранилища модуля аккаунтов
// (чтение без блокировки).
type BillingOrganizations struct{ Orgs *accountpg.Organizations }

var _ domain.Organizations = BillingOrganizations{}

func toRequisites(d accountpg.BillingDetails) domain.OrgRequisites {
	return domain.OrgRequisites{
		ID: d.ID, Name: d.Name, INN: d.INN, KPP: d.KPP, Address: d.Address, BankAccount: d.BankAccount,
		BankName: d.BankName, BankBIK: d.BankBIK, BankCorrAccount: d.BankCorrAccount, IsPlatform: d.IsPlatform,
	}
}

func (b BillingOrganizations) Requisites(ctx context.Context, orgID uuid.UUID) (domain.OrgRequisites, error) {
	d, err := b.Orgs.ForBilling(ctx, orgID)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return domain.OrgRequisites{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OrgRequisites{}, err
	}
	return toRequisites(d), nil
}

func (b BillingOrganizations) ByINN(ctx context.Context, inn string) (domain.OrgRequisites, error) {
	d, err := b.Orgs.ForBillingByINN(ctx, inn)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return domain.OrgRequisites{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OrgRequisites{}, err
	}
	return toRequisites(d), nil
}

// ---------------------------------------------------------------- billing ← ledger

// BillingLedger — billing/domain.Ledger поверх журнала проводок.
type BillingLedger struct{ Svc *ledgerapp.Service }

var _ domain.Ledger = BillingLedger{}

func (b BillingLedger) Deposit(ctx context.Context, d domain.DepositRequest) error {
	_, err := b.Svc.Deposit(ctx, d.BankTransactionID, d.InvoiceID, d.OrganizationID, d.Amount, d.AdminID)
	return err
}

func (b BillingLedger) Settlements(ctx context.Context, from, to time.Time) ([]domain.Settlement, error) {
	lines, err := b.Svc.Settlements(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Settlement, 0, len(lines))
	for _, l := range lines {
		out = append(out, domain.Settlement{PlacementID: l.PlacementID, AdvertiserOrgID: l.AdvertiserID,
			Payout: l.Payout, Fee: l.Fee, SettledAt: l.SettledAt})
	}
	return out, nil
}

// ---------------------------------------------------------------- billing ← placement

// BillingPlacements — сведения о размещениях для отчёта агента (R-16).
type BillingPlacements struct{ Placements *placementpg.Placements }

var _ domain.Placements = BillingPlacements{}

func (b BillingPlacements) Info(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.PlacementInfo, error) {
	refs, err := b.Placements.Refs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.PlacementInfo, len(refs))
	for id, r := range refs {
		out[id] = domain.PlacementInfo{OrderNumber: r.OrderNumber, ChannelTitle: r.ChannelTitle, PublishedAt: r.PublishedAt,
			RemovedAt: r.RemovedAt}
	}
	return out, nil
}
