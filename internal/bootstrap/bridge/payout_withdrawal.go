package bridge

import (
	"context"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func (p PayoutProvider) CreateWithdrawal(ctx context.Context, external string, t domain.WithdrawalTerms) (domain.ProviderTask, error) {
	api, ok := p.API.(interface {
		CreateWithdrawal(context.Context, rocketwork.WithdrawalTerms) (rocketwork.TaskSnapshot, error)
	})
	if !ok {
		return domain.ProviderTask{}, domain.ErrProviderDisabled
	}
	d := t.Destination
	r, err := api.CreateWithdrawal(ctx, rocketwork.WithdrawalTerms{ExternalID: external, CompanyINN: d.CompanyINN, BIC: d.BIC, AccountNumber: d.AccountNumber, ExecutorID: d.ExecutorID, AmountKopecks: int64(t.Amount), MaxCommissionKopecks: int64(t.MaxCommission)})
	return providerTask(r), providerErr(err)
}

func (l PayoutLedger) Withdrawal(ctx context.Context, w domain.Withdrawal, fee money.Kopecks) error {
	postings := []ledgerdomain.Posting{{Debit: ledgerdomain.Bank(), Credit: ledgerdomain.RocketworkDeposit(), Amount: w.Terms.Amount}}
	if fee > 0 {
		postings = append(postings, ledgerdomain.Posting{Debit: ledgerdomain.PayoutFees(), Credit: ledgerdomain.RocketworkDeposit(), Amount: fee})
	}
	_, err := l.Svc.Post(ctx, ledgerdomain.NewManual("rocketwork-withdrawal:"+w.ID.String(), postings, w.Terms.AdminID, w.Terms.Reason))
	return err
}

func (l PayoutLedger) RecordPayoutReturn(ctx context.Context, p domain.Payout, e domain.ReturnEvidence) error {
	debit := ledgerdomain.RocketworkDeposit()
	if e.Destination == "bank" {
		debit = ledgerdomain.Bank()
	}
	_, err := l.Svc.Post(ctx, ledgerdomain.NewManual("payout-return:"+e.ID.String(), []ledgerdomain.Posting{{Debit: debit, Credit: ledgerdomain.BloggerPayable(p.BloggerOrganizationID), Amount: e.Amount}}, e.AdminID, e.Reason))
	return err
}
