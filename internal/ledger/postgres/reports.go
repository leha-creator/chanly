package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var _ domain.Reports = (*Store)(nil)

func uuidOf(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// Entries — проводки счёта; счёта ещё нет — пустой список.
func (s *Store) Entries(ctx context.Context, ref domain.AccountRef, after *domain.EntryCursor, limit int) ([]domain.Entry, error) {
	acc, err := s.account(ctx, ref)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	params := sqlc.ListAccountEntriesParams{AccountID: acc.ID, Lim: int32(limit)}
	if after != nil {
		params.HasCursor, params.CursorAt, params.CursorID = true, after.At, after.ID
	}
	rows, err := s.q(ctx).ListAccountEntries(ctx, params)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.Entries", err)
	}
	out := make([]domain.Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Entry{
			ID: r.ID, TransactionID: r.TransactionID, Kind: domain.Kind(r.Kind), Amount: money.Kopecks(r.Amount),
			Credit: r.IsCredit, PlacementID: uuidOf(r.PlacementID), InvoiceID: uuidOf(r.InvoiceID), CreatedAt: r.CreatedAt.UTC(),
		})
	}
	return out, nil
}

func (s *Store) Settlements(ctx context.Context, from, to time.Time) ([]domain.SettlementLine, error) {
	rows, err := s.q(ctx).ListSettlements(ctx, sqlc.ListSettlementsParams{FromAt: from, ToAt: to})
	if err != nil {
		return nil, s.fail(ctx, "Ledger.Settlements", err)
	}
	out := make([]domain.SettlementLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.SettlementLine{PlacementID: r.PlacementID, AdvertiserID: r.AdvertiserID,
			Payout: money.Kopecks(r.Payout), Fee: money.Kopecks(r.Fee), SettledAt: r.CreatedAt.UTC()})
	}
	return out, nil
}

func (s *Store) SidesTotal(ctx context.Context) (money.Kopecks, money.Kopecks, error) {
	r, err := s.q(ctx).SumBalancesBySide(ctx)
	if err != nil {
		return 0, 0, s.fail(ctx, "Ledger.SidesTotal", err)
	}
	return money.Kopecks(r.Assets), money.Kopecks(r.Liabilities), nil
}

func (s *Store) CacheMismatches(ctx context.Context) ([]domain.CacheMismatch, error) {
	rows, err := s.q(ctx).ListCacheMismatches(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.CacheMismatches", err)
	}
	out := make([]domain.CacheMismatch, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.CacheMismatch{AccountID: r.ID, Kind: domain.AccountKind(r.Kind),
			Cached: money.Kopecks(r.Balance), Computed: money.Kopecks(r.Computed)})
	}
	return out, nil
}

func (s *Store) HoldMismatches(ctx context.Context) ([]domain.HoldMismatch, error) {
	rows, err := s.q(ctx).ListHoldMismatches(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.HoldMismatches", err)
	}
	out := make([]domain.HoldMismatch, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.HoldMismatch{PlacementID: r.PlacementID, Balance: money.Kopecks(r.Balance), Held: money.Kopecks(r.Held)})
	}
	return out, nil
}

func (s *Store) NegativeLiabilities(ctx context.Context) ([]domain.NegativeAccount, error) {
	rows, err := s.q(ctx).ListNegativeLiabilities(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.NegativeLiabilities", err)
	}
	out := make([]domain.NegativeAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.NegativeAccount{AccountID: r.ID, Kind: domain.AccountKind(r.Kind), Balance: money.Kopecks(r.Balance)})
	}
	return out, nil
}

// account — строка счёта без блокировки; нет — domain.ErrNotFound.
func (s *Store) account(ctx context.Context, ref domain.AccountRef) (sqlc.LedgerAccount, error) {
	row, err := s.q(ctx).GetLedgerAccount(ctx, sqlc.GetLedgerAccountParams{
		Kind: string(ref.Kind), OrganizationID: uuidPtr(ref.OrganizationID), PlacementID: uuidPtr(ref.PlacementID),
	})
	if err != nil {
		return sqlc.LedgerAccount{}, s.fail(ctx, "Ledger.Account", err)
	}
	return row, nil
}

func (s *Store) Journal(ctx context.Context, f domain.JournalFilter) ([]domain.JournalLine, error) {
	rows, err := s.q(ctx).ListJournal(ctx, sqlc.ListJournalParams{
		HasOrg: f.OrganizationID != uuid.Nil, OrgID: f.OrganizationID, Kind: string(f.Kind), Since: f.Since, Lim: int32(f.Limit),
	})
	if err != nil {
		return nil, s.fail(ctx, "Ledger.Journal", err)
	}
	out := make([]domain.JournalLine, 0, len(rows))
	for _, r := range rows {
		line := domain.JournalLine{
			TransactionID: r.TransactionID, Kind: domain.Kind(r.Kind), CreatedAt: r.CreatedAt.UTC(), Amount: money.Kopecks(r.Amount),
			Debit: domain.AccountRef{Kind: domain.AccountKind(r.DebitKind), OrganizationID: uuidOf(r.DebitOrg),
				PlacementID: uuidOf(r.DebitPlacement)},
			Credit: domain.AccountRef{Kind: domain.AccountKind(r.CreditKind), OrganizationID: uuidOf(r.CreditOrg),
				PlacementID: uuidOf(r.CreditPlacement)},
		}
		if r.Reason != nil {
			line.Reason = *r.Reason
		}
		out = append(out, line)
	}
	return out, nil
}

var _ domain.AdminJournal = (*Store)(nil)

// AdminEntries — журнал денег администратора (adm/ledger) со смещением и общим числом.
func (s *Store) AdminEntries(ctx context.Context, f domain.AdminEntryFilter) ([]domain.AdminEntry, int, error) {
	var kind *string
	if f.Kind != nil {
		k := string(*f.Kind)
		kind = &k
	}
	q := s.q(ctx)
	rows, err := q.ListAdminEntries(ctx, sqlc.ListAdminEntriesParams{OrgID: f.OrganizationID, Kind: kind,
		CreatedFrom: f.From, CreatedTo: f.To, Off: int32(f.Offset), Lim: int32(f.Limit)})
	if err != nil {
		return nil, 0, s.fail(ctx, "Ledger.AdminEntries", err)
	}
	total, err := q.CountAdminEntries(ctx, sqlc.CountAdminEntriesParams{OrgID: f.OrganizationID, Kind: kind,
		CreatedFrom: f.From, CreatedTo: f.To})
	if err != nil {
		return nil, 0, s.fail(ctx, "Ledger.CountAdminEntries", err)
	}
	out := make([]domain.AdminEntry, 0, len(rows))
	for _, r := range rows {
		e := domain.AdminEntry{
			JournalLine: domain.JournalLine{TransactionID: r.TransactionID, Kind: domain.Kind(r.Kind), CreatedAt: r.CreatedAt.UTC(),
				Amount: money.Kopecks(r.Amount),
				Debit: domain.AccountRef{Kind: domain.AccountKind(r.DebitKind), OrganizationID: uuidOf(r.DebitOrg),
					PlacementID: uuidOf(r.DebitPlacement)},
				Credit: domain.AccountRef{Kind: domain.AccountKind(r.CreditKind), OrganizationID: uuidOf(r.CreditOrg),
					PlacementID: uuidOf(r.CreditPlacement)}},
			ActorType: domain.ActorType(r.ActorType), ActorUserID: r.ActorUserID, ApproverID: r.ApproverUserID,
			PlacementID: r.PlacementID, InvoiceID: r.InvoiceID, PayoutID: r.PayoutID,
		}
		if r.Reason != nil {
			e.Reason = *r.Reason
		}
		out = append(out, e)
	}
	return out, int(total), nil
}
