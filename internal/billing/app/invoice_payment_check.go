package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

const paymentCheckInterval = time.Minute

type PaymentCheck struct {
	Invoice    domain.Invoice
	Result     string
	CheckedAt  *time.Time
	RetryAfter int
}

// CheckInvoicePayment never trusts a client claim of payment. Only a confirmed
// statement operation can credit the ledger through the regular ingestion path.
func (s *Service) CheckInvoicePayment(ctx context.Context, orgID, invoiceID uuid.UUID) (PaymentCheck, error) {
	inv, err := s.Invoices.Get(ctx, orgID, invoiceID)
	if err != nil {
		return PaymentCheck{}, notFound(err, "invoice")
	}
	result := PaymentCheck{Invoice: inv, Result: "unavailable", RetryAfter: 60}
	if inv.Status == domain.InvoicePaid {
		result.Result = "paid"
		result.RetryAfter = 0
		return result, nil
	}
	if !inv.Payable() {
		return result, apperr.InvalidTransition("check_payment", string(inv.Status))
	}
	if s.BankSource == nil || s.BankSync == nil || s.BankIdentity == nil || (s.BankConfig.Access != domain.BankAutomation && !s.BankConfig.DevPaymentsEnabled()) {
		return result, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	readCtx = audit.WithActor(readCtx, audit.Actor{Type: audit.ActorSystem})
	err = s.BankSync.WithBankLock(readCtx, s.Config.Supplier.BankAccount, func(ctx context.Context) error {
		p, err := s.BankSync.LoadBankProgress(ctx, s.Config.Supplier.BankAccount)
		if err != nil {
			return err
		}
		now := s.Clock.Now().UTC()
		if remaining := paymentCheckInterval - now.Sub(p.LastManualCheck); !p.LastManualCheck.IsZero() && remaining > 0 {
			result.Result = "busy"
			result.RetryAfter = int((remaining + time.Second - 1) / time.Second)
			return nil
		}
		// Reserve before the network request, including failed requests. The
		// account lock makes this cooldown shared by users and API replicas.
		p.LastManualCheck = now
		if err := s.BankSync.SaveBankProgress(ctx, p); err != nil {
			return err
		}
		if err := s.readRecentPayments(ctx, inv.CreatedAt, now); err != nil {
			return err
		}
		result.Result = "not_found"
		result.CheckedAt = &now
		return nil
	})
	if errors.Is(err, domain.ErrBankSyncBusy) {
		result.Result = "busy"
		result.RetryAfter = 5
	} else if err != nil {
		s.log.WarnContext(ctx, "invoice payment check unavailable", "invoice_id", invoiceID)
		result.Result = "unavailable"
	}
	// A concurrent background import or a partially completed request may have
	// confirmed payment even if the remaining pages could not be read.
	current, getErr := s.Invoices.Get(ctx, orgID, invoiceID)
	if getErr != nil {
		return PaymentCheck{}, getErr
	}
	result.Invoice = current
	if current.Status == domain.InvoicePaid {
		result.Result = "paid"
		result.RetryAfter = 0
	}
	s.log.InfoContext(ctx, "invoice payment checked", "invoice_id", invoiceID, "result", result.Result)
	return result, nil
}

// A manual check reads from invoice issuance (up to the bank's 31-day window),
// not an old pending background interval. One minute covers timestamp rounding.
// It deliberately preserves the background checkpoint and reconciliation state.
func (s *Service) readRecentPayments(ctx context.Context, issuedAt, now time.Time) error {
	from := issuedAt.Add(-time.Minute)
	if earliest := now.Add(-31 * 24 * time.Hour); from.Before(earliest) {
		from = earliest
	}
	if from.Before(s.BankConfig.Start) {
		from = s.BankConfig.Start
	}
	if !from.Before(now) {
		return errors.New("bank import start is not in the past")
	}
	id := ids.New()
	st := domain.BankStatement{ID: id, Source: "tbank", Account: s.Config.Supplier.BankAccount, DateFrom: &from, DateTo: &now, CreatedAt: now, UpdatedAt: now}
	if err := s.Statements.Insert(ctx, st); err != nil {
		return err
	}
	query := domain.BankQuery{Account: st.Account, From: from, To: now}
	cursors := map[string]bool{"": true}
	for pages := 0; pages < 10; pages++ {
		page, err := s.BankSource.ReadPage(ctx, query)
		if err != nil {
			return err
		}
		if page.NextCursor != "" && cursors[page.NextCursor] {
			return errors.New("bank returned repeated cursor")
		}
		if pages == 0 {
			st.Opening, st.Closing = page.Opening, page.Closing
		}
		for _, in := range page.Incoming {
			outcome, err := s.importIncoming(ctx, "Billing.CheckInvoicePayment", id, in)
			if err != nil {
				return err
			}
			st.IncomingCount++
			switch {
			case outcome.duplicate:
				st.DuplicateCount++
			case outcome.matched:
				st.MatchedCount++
			default:
				st.UnmatchedCount++
			}
		}
		st.UpdatedAt = s.Clock.Now()
		done := page.NextCursor == ""
		if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
			if err := s.Statements.UpdateCounts(ctx, st); err != nil {
				return err
			}
			if done {
				return s.Statements.Complete(ctx, id)
			}
			return nil
		}); err != nil {
			return err
		}
		if done {
			return nil
		}
		cursors[page.NextCursor] = true
		query.Cursor = page.NextCursor
	}
	return errors.New("manual bank check page limit exceeded")
}
