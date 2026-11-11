package app

import (
	"context"
	"errors"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func (s *Service) authorizeBankCredit(ctx context.Context, bt domain.BankTransaction) error {
	var err error
	if s.BankConfig.Access == domain.BankControlled {
		if !s.BankConfig.DevPaymentsEnabled() {
			err = domain.DenyBankCredit("dev_real_disabled")
		} else if !domain.HasDevPaymentMarker(bt.Purpose, s.BankConfig.DevPaymentCode) {
			err = domain.DenyBankCredit("dev_marker_missing")
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
