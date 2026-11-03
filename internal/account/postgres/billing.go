package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// BillingDetails — реквизиты организации для счёта и закрывающих документов (модуль billing).
type BillingDetails struct {
	ID              uuid.UUID
	Name            string
	INN             string
	KPP             string
	Address         string
	BankAccount     string
	BankName        string
	BankBIK         string
	BankCorrAccount string
	IsPlatform      bool
}

func billingDetails(o sqlc.Organization) BillingDetails {
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return BillingDetails{
		ID: o.ID, Name: o.Name, INN: o.Inn, KPP: s(o.Kpp), Address: s(o.Address), BankAccount: s(o.BankAccount),
		BankName: s(o.BankName), BankBIK: s(o.BankBik), BankCorrAccount: s(o.BankCorrAccount), IsPlatform: o.IsPlatform,
	}
}

// ForBilling читает реквизиты без блокировки; нет организации — domain.ErrNotFound.
func (r *Organizations) ForBilling(ctx context.Context, id uuid.UUID) (BillingDetails, error) {
	const op = "Account.Organizations.ForBilling"
	row, err := r.s.q(ctx).GetOrganizationByID(ctx, id)
	if err != nil {
		return BillingDetails{}, r.s.fail(ctx, op, err)
	}
	return billingDetails(row), nil
}

// ForBillingByINN — организация по ИНН (ручное сопоставление поступления); нет — domain.ErrNotFound.
func (r *Organizations) ForBillingByINN(ctx context.Context, inn string) (BillingDetails, error) {
	const op = "Account.Organizations.ForBillingByINN"
	row, err := r.s.q(ctx).GetOrganizationByINN(ctx, inn)
	if err != nil {
		return BillingDetails{}, r.s.fail(ctx, op, err)
	}
	return billingDetails(row), nil
}
