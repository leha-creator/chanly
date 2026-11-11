package app

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

const KindSyncBank = "sync_bank_statements"

type BankConfig struct {
	Access                      string
	CreditPolicy                domain.BankCreditPolicy
	Start                       time.Time
	Period, Overlap, StaleAfter time.Duration
}

// SyncBank reads at most one fixed period per scheduled run. Backlog is resumed
// on subsequent runs; a page checkpoint is written only after every operation.
func (s *Service) SyncBank(ctx context.Context) (res ImportResult, err error) {
	if s.BankSource == nil || s.BankSync == nil || s.BankIdentity == nil {
		return res, errors.New("bank sync disabled")
	}
	if s.BankConfig.Access != domain.BankControlled && s.BankConfig.Access != domain.BankAutomation {
		return res, domain.DenyBankCredit("sync_requires_credit_access")
	}
	if s.BankConfig.Period <= 0 || s.BankConfig.Overlap < 0 || s.BankConfig.Start.IsZero() {
		return res, errors.New("invalid bank synchronization settings")
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})
	runID := ids.New()
	s.log.DebugContext(ctx, "bank sync started", "run_id", runID)
	err = s.BankSync.WithBankLock(ctx, s.Config.Supplier.BankAccount, func(ctx context.Context) error {
		p, err := s.BankSync.LoadBankProgress(ctx, s.Config.Supplier.BankAccount)
		if err != nil {
			return err
		}
		if !p.LastSuccess.IsZero() && s.Clock.Now().Sub(p.LastSuccess) > s.BankConfig.StaleAfter {
			s.raise(ctx, "Billing.SyncBank", alert.Alert{Reason: "bank_sync_stale", ObjectKey: "tbank", Level: alert.LevelWarn, Summary: "Давно не было успешной синхронизации банка"})
		}
		if p.To.IsZero() {
			from := p.CompletedTo
			if from.IsZero() {
				from = s.BankConfig.Start
			}
			if from.IsZero() {
				return errors.New("bank import start required")
			}
			to := from.Add(s.BankConfig.Period)
			now := s.Clock.Now().UTC()
			if to.After(now) {
				to = now
			}
			if !from.Before(to) {
				return nil
			}
			p.From = from.Add(-s.BankConfig.Overlap)
			if p.From.Before(s.BankConfig.Start) {
				p.From = s.BankConfig.Start
			}
			p.To = to
			p.StatementID = ids.New().String()
			p.Cursor = ""
			p.Cursors = nil
			// Insert header and checkpoint atomically, preventing orphaned imports.
			if err = s.Tx.InTx(ctx, func(ctx context.Context) error {
				id, _ := uuid.Parse(p.StatementID)
				st := domain.BankStatement{ID: id, Source: "tbank", Account: p.Account, DateFrom: &p.From, DateTo: &p.To, CreatedAt: now, UpdatedAt: now}
				if err := s.Statements.Insert(ctx, st); err != nil {
					return err
				}
				return s.BankSync.SaveBankProgress(ctx, p)
			}); err != nil {
				return err
			}
		}
		id, err := uuid.Parse(p.StatementID)
		if err != nil {
			return errors.New("invalid bank checkpoint")
		}
		res.StatementID = id
		for pages := 0; pages < 1000; pages++ {
			page, err := s.BankSource.ReadPage(ctx, domain.BankQuery{Account: p.Account, From: p.From, To: p.To, Cursor: p.Cursor})
			if err != nil {
				return err
			}
			if page.NextCursor != "" {
				for _, cursor := range append(p.Cursors, p.Cursor) {
					if page.NextCursor == cursor {
						return errors.New("bank returned repeated cursor")
					}
				}
			}
			st, err := s.Statements.Get(ctx, id)
			if err != nil {
				return err
			}
			if p.Cursor == "" {
				st.Opening, st.Closing = page.Opening, page.Closing
				p.BalanceMismatch = bankBalanceMismatch(page)
				if p.BalanceMismatch {
					s.raise(ctx, "Billing.SyncBank", alert.Alert{Reason: "bank_balance_mismatch", ObjectKey: id.String(), Level: alert.LevelWarn, Summary: "Остатки и обороты банковского периода не сходятся"})
				}
			}
			matchedBefore, unmatchedBefore, duplicatesBefore := res.Matched, res.Unmatched, res.Duplicates
			for _, in := range page.Incoming {
				outcome, err := s.importIncoming(ctx, "Billing.SyncBank", id, in)
				if err != nil {
					return err
				}
				res.Incoming++
				switch {
				case outcome.conflict:
					res.Conflicts++
				case outcome.duplicate:
					res.Duplicates++
				case outcome.matched:
					res.Matched++
					res.Credited += in.Amount
				default:
					res.Unmatched++
				}
			}
			st.IncomingCount += len(page.Incoming)
			st.MatchedCount += res.Matched - matchedBefore
			st.UnmatchedCount += res.Unmatched - unmatchedBefore
			st.DuplicateCount += res.Duplicates - duplicatesBefore
			st.UpdatedAt = s.Clock.Now()
			p.Cursors = append(p.Cursors, p.Cursor)
			p.Cursor = page.NextCursor
			done := page.NextCursor == ""
			if done {
				p.LedgerDifference = nil
				// A historical bank closing must not be compared with today's ledger.
				// Reconcile current balances once the backlog reaches a fresh interval.
				if age := s.Clock.Now().Sub(p.To); age >= 0 && age <= time.Minute && st.Closing != nil {
					p.LedgerDifference, err = s.reconcileBankLedger(ctx, *st.Closing)
					if err != nil {
						return err
					}
					if *p.LedgerDifference != 0 {
						s.raise(ctx, "Billing.SyncBank", alert.Alert{Reason: "bank_ledger_mismatch", ObjectKey: id.String(), Level: alert.LevelWarn, Summary: "Остаток банка не совпадает с журналом с учётом несопоставленных поступлений", Fields: map[string]any{"difference": int64(*p.LedgerDifference)}})
					}
				}
				p.Missing, err = s.BankSync.MissingBankOperations(ctx, p.Account, p.From, p.To, id)
				if err != nil {
					return err
				}
				if p.Missing > 0 {
					s.raise(ctx, "Billing.SyncBank", alert.Alert{Reason: "bank_operations_missing", ObjectKey: id.String(), Level: alert.LevelWarn, Summary: "Ранее учтённые операции отсутствуют в повторной выписке", Fields: map[string]any{"missing": p.Missing}})
				}
				if p.To.After(p.CompletedTo) {
					p.CompletedTo = p.To
				}
				p.LastSuccess = s.Clock.Now().UTC()
				p.From = time.Time{}
				p.To = time.Time{}
				p.Cursor = ""
				p.Cursors = nil
				p.StatementID = ""
			}
			if err = s.Tx.InTx(ctx, func(ctx context.Context) error {
				if err := s.Statements.UpdateCounts(ctx, st); err != nil {
					return err
				}
				if done {
					if err := s.Statements.Complete(ctx, id); err != nil {
						return err
					}
				}
				if err := s.Audit.Record(ctx, audit.Entry{Action: "bank.sync.page", ObjectType: "bank_statement", ObjectID: id.String(), ActorType: audit.ActorSystem, Data: map[string]any{"operations": page.Operations, "rejected": page.Rejected, "complete": done}}); err != nil {
					return err
				}
				return s.BankSync.SaveBankProgress(ctx, p)
			}); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "bank page committed", "run_id", runID, "statement_id", id, "incoming", len(page.Incoming), "matched", res.Matched, "duplicates", res.Duplicates, "conflicts", res.Conflicts, "complete", done)
			if done {
				return nil
			}
		}
		return errors.New("bank page limit exceeded")
	})
	if err != nil {
		s.log.ErrorContext(ctx, "bank synchronization failed", "run_id", runID)
		s.raise(ctx, "Billing.SyncBank", alert.Alert{Reason: "bank_sync_failed", ObjectKey: "tbank", Level: alert.LevelWarn, Summary: "Синхронизация банка не завершена; период будет повторён"})
	} else if s.Alerts != nil {
		_ = s.Alerts.Resolve(ctx, "bank_sync_failed", "tbank")
		_ = s.Alerts.Resolve(ctx, "bank_sync_stale", "tbank")
	}
	return res, err
}

// PrepareBankReplay preserves the high-water mark and records a fixed historic
// interval before network activity. A pending different interval cannot be replaced.
func (s *Service) PrepareBankReplay(ctx context.Context, from, to time.Time) error {
	if s.BankConfig.Access != domain.BankControlled && s.BankConfig.Access != domain.BankAutomation {
		return domain.DenyBankCredit("replay_requires_credit_access")
	}
	if s.BankSource == nil || s.BankSync == nil || s.BankIdentity == nil || !from.Before(to) || from.Before(s.BankConfig.Start) || to.After(s.Clock.Now()) {
		return errors.New("invalid bank replay interval")
	}
	return s.BankSync.WithBankLock(ctx, s.Config.Supplier.BankAccount, func(ctx context.Context) error {
		p, err := s.BankSync.LoadBankProgress(ctx, s.Config.Supplier.BankAccount)
		if err != nil {
			return err
		}
		if !p.To.IsZero() {
			if p.From.Equal(from) && p.To.Equal(to) {
				return nil
			}
			return errors.New("finish pending bank period before replay")
		}
		p.From = from.UTC()
		p.To = to.UTC()
		id := ids.New()
		p.StatementID = id.String()
		p.Cursor = ""
		p.Cursors = nil
		now := s.Clock.Now()
		return s.Tx.InTx(ctx, func(ctx context.Context) error {
			if err := s.Statements.Insert(ctx, domain.BankStatement{ID: id, Source: "tbank", Account: p.Account, DateFrom: &p.From, DateTo: &p.To, CreatedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			return s.BankSync.SaveBankProgress(ctx, p)
		})
	})
}

func bankBalanceMismatch(p domain.BankPage) bool {
	if p.Opening == nil || p.Closing == nil || p.Credit == nil || p.Debit == nil {
		return false
	}
	value := func(k *money.Kopecks) *big.Int { return big.NewInt(int64(*k)) }
	expected := new(big.Int).Add(value(p.Opening), value(p.Credit))
	expected.Sub(expected, value(p.Debit))
	return expected.Cmp(value(p.Closing)) != 0
}

func (s *Service) reconcileBankLedger(ctx context.Context, closing money.Kopecks) (*money.Kopecks, error) {
	balance, err := s.Ledger.BankBalance(ctx)
	if err != nil {
		return nil, err
	}
	_, unmatched, err := s.BankTransactions.UnmatchedTotal(ctx)
	if err != nil {
		return nil, err
	}
	difference := new(big.Int).Sub(big.NewInt(int64(closing)), big.NewInt(int64(balance)))
	difference.Sub(difference, big.NewInt(int64(unmatched)))
	if !difference.IsInt64() {
		return nil, errors.New("bank reconciliation amount overflow")
	}
	result := money.Kopecks(difference.Int64())
	return &result, nil
}
