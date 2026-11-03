// Package domain — правила журнала проводок (06 §2): счета учёта, виды транзакций,
// допустимые пары счетов, остатки. Без базы, сети и журнала.
package domain

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// AccountKind — вид счёта учёта (06 §2.1, ledger_account.kind).
type AccountKind string

const (
	AccountBank              AccountKind = "bank"
	AccountRocketworkDeposit AccountKind = "rocketwork_deposit"
	AccountPayoutFees        AccountKind = "payout_fees"
	AccountAdvertiserBalance AccountKind = "advertiser_balance"
	AccountPlacementHold     AccountKind = "placement_hold"
	AccountBloggerPayable    AccountKind = "blogger_payable"
	AccountPlatformRevenue   AccountKind = "platform_revenue"
)

// AccountKinds — все виды в порядке 06 §2.1.
var AccountKinds = []AccountKind{
	AccountBank, AccountRocketworkDeposit, AccountPayoutFees,
	AccountAdvertiserBalance, AccountPlacementHold, AccountBloggerPayable, AccountPlatformRevenue,
}

// Side — тип счёта: актив или пассив (06 §2.1).
type Side int

const (
	Asset Side = iota + 1
	Liability
)

// Valid — вид из перечня 06 §2.1.
func (k AccountKind) Valid() bool {
	for _, v := range AccountKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Side — актив или пассив. payout_fees — актив (расход).
func (k AccountKind) Side() Side {
	switch k {
	case AccountBank, AccountRocketworkDeposit, AccountPayoutFees:
		return Asset
	}
	return Liability
}

// ScopedByOrganization — разрез счёта — организация.
func (k AccountKind) ScopedByOrganization() bool {
	return k == AccountAdvertiserBalance || k == AccountBloggerPayable
}

// ScopedByPlacement — разрез счёта — размещение.
func (k AccountKind) ScopedByPlacement() bool { return k == AccountPlacementHold }

// AccountRef — счёт по виду и разрезу. Пустой uuid — разреза нет.
type AccountRef struct {
	Kind           AccountKind
	OrganizationID uuid.UUID
	PlacementID    uuid.UUID
}

// Bank, PlatformRevenue … — счета без разреза.
func Bank() AccountRef              { return AccountRef{Kind: AccountBank} }
func RocketworkDeposit() AccountRef { return AccountRef{Kind: AccountRocketworkDeposit} }
func PayoutFees() AccountRef        { return AccountRef{Kind: AccountPayoutFees} }
func PlatformRevenue() AccountRef   { return AccountRef{Kind: AccountPlatformRevenue} }

// AdvertiserBalance — свободные деньги рекламодателя.
func AdvertiserBalance(orgID uuid.UUID) AccountRef {
	return AccountRef{Kind: AccountAdvertiserBalance, OrganizationID: orgID}
}

// BloggerPayable — начислено блогеру.
func BloggerPayable(orgID uuid.UUID) AccountRef {
	return AccountRef{Kind: AccountBloggerPayable, OrganizationID: orgID}
}

// PlacementHold — удержание по размещению.
func PlacementHold(placementID uuid.UUID) AccountRef {
	return AccountRef{Kind: AccountPlacementHold, PlacementID: placementID}
}

// Validate — вид известен, разрез соответствует виду (ledger_account_scope).
func (r AccountRef) Validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("%w: unknown account kind %q", ErrInvalidPosting, r.Kind)
	}
	if r.Kind.ScopedByOrganization() != (r.OrganizationID != uuid.Nil) {
		return fmt.Errorf("%w: account %s: organization scope mismatch", ErrInvalidPosting, r.Kind)
	}
	if r.Kind.ScopedByPlacement() != (r.PlacementID != uuid.Nil) {
		return fmt.Errorf("%w: account %s: placement scope mismatch", ErrInvalidPosting, r.Kind)
	}
	return nil
}

// String — вид и разрез: bank, advertiser_balance:{org}, placement_hold:{placement}.
func (r AccountRef) String() string {
	switch {
	case r.OrganizationID != uuid.Nil:
		return string(r.Kind) + ":" + r.OrganizationID.String()
	case r.PlacementID != uuid.Nil:
		return string(r.Kind) + ":" + r.PlacementID.String()
	}
	return string(r.Kind)
}

// Delta — изменение остатка счёта стороны side от проводки (06 §2.2 п. 2): актив растёт
// по дебету, пассив — по кредиту.
func Delta(side Side, debit bool, amount money.Kopecks) money.Kopecks {
	if (side == Asset) == debit {
		return amount
	}
	return -amount
}
