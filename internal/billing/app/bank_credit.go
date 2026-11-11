package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func (s *Service) authorizeBankCredit(ctx context.Context, bt domain.BankTransaction, now time.Time) error {
	var err error
	if s.BankConfig.Access == domain.BankControlled {
		p := s.BankConfig.CreditPolicy
		if p.Account != s.Config.Supplier.BankAccount {
			err = domain.DenyBankCredit("account_not_allowed")
		} else {
			err = p.Check(bt.OrganizationID, bt.Amount, now)
		}
		if err == nil && !strings.Contains(strings.ToLower(bt.Purpose), strings.ToLower(p.Marker())) {
			err = domain.DenyBankCredit("run_marker_missing")
		}
		if err == nil {
			if s.BankBudget == nil {
				err = domain.DenyBankCredit("budget_unavailable")
			} else {
				err = s.BankBudget.ReserveBankCredit(ctx, p, bt.ID, bt.Amount, now)
			}
		}
	} else if s.BankConfig.Access != domain.BankAutomation {
		statement, e := s.Statements.Get(ctx, bt.StatementID)
		if e != nil {
			return e
		}
		if statement.Source == "tbank" {
			err = domain.DenyBankCredit("readonly")
		}
	}
	if err != nil {
		if !errors.Is(err, domain.ErrBankCreditRestricted) {
			return err
		}
		return apperr.Conflict("bank credit is not permitted").WithCause(err)
	}
	return nil
}
