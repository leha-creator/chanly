package domain

import (
	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Ключи идемпотентности (06 §2.2 п. 7, R-3): вид и основание.

// DepositKey — зачисление поступления из выписки.
func DepositKey(bankTxID uuid.UUID) string { return "deposit:" + bankTxID.String() }

// HoldKey — удержание по размещению и версии поста, с которой оно создано.
func HoldKey(placementID, versionID uuid.UUID) string {
	return "hold:" + placementID.String() + ":" + versionID.String()
}

// HoldReleaseKey — снятие удержания этой версии.
func HoldReleaseKey(placementID, versionID uuid.UUID) string {
	return "hold_release:" + placementID.String() + ":" + versionID.String()
}

// SettlementKey — распределение удержания размещения.
func SettlementKey(placementID uuid.UUID) string { return "settlement:" + placementID.String() }

// PayoutKey — выплата блогеру.
func PayoutKey(payoutID uuid.UUID) string { return "payout:" + payoutID.String() }

// OperationKey — ключ ручной операции администратора: вид и ключ команды.
func OperationKey(kind Kind, key string) string { return string(kind) + ":" + key }

// NewDeposit — поступление по счёту сопоставлено (S-30): bank → advertiser_balance на фактическую сумму.
func NewDeposit(bankTxID, invoiceID, orgID uuid.UUID, amount money.Kopecks, actor ActorType, actorUserID uuid.UUID) Transaction {
	return Transaction{
		Kind:           KindDeposit,
		Basis:          Basis{BankTransactionID: bankTxID, InvoiceID: invoiceID},
		Actor:          actor,
		ActorUserID:    actorUserID,
		IdempotencyKey: DepositKey(bankTxID),
		Postings:       []Posting{{Debit: Bank(), Credit: AdvertiserBalance(orgID), Amount: amount}},
	}
}

// NewHold — удержание суммы размещения с баланса рекламодателя (P-01, новая версия поста).
func NewHold(placementID, versionID, orgID uuid.UUID, amount money.Kopecks) Transaction {
	return Transaction{
		Kind:           KindHold,
		Basis:          Basis{PlacementID: placementID},
		Actor:          ActorSystem,
		IdempotencyKey: HoldKey(placementID, versionID),
		Postings:       []Posting{{Debit: AdvertiserBalance(orgID), Credit: PlacementHold(placementID), Amount: amount}},
	}
}

// NewHoldRelease — возврат остатка удержания на баланс.
func NewHoldRelease(placementID, versionID, orgID uuid.UUID, amount money.Kopecks) Transaction {
	return Transaction{
		Kind:           KindHoldRelease,
		Basis:          Basis{PlacementID: placementID},
		Actor:          ActorSystem,
		IdempotencyKey: HoldReleaseKey(placementID, versionID),
		Postings:       []Posting{{Debit: PlacementHold(placementID), Credit: AdvertiserBalance(orgID), Amount: amount}},
	}
}

// Distribution — распределение удержания (03 §7): блогеру, комиссия, возврат.
type Distribution struct {
	Payout     money.Kopecks
	FeeCharged money.Kopecks
	Refund     money.Kopecks
}

// Total — сумма частей; равна удержанию (I-12).
func (d Distribution) Total() money.Kopecks { return d.Payout + d.FeeCharged + d.Refund }

// NewSettlement — распределение удержания при приёмке или решении спора (P-12, P-13, P-15).
// Нулевые части не проводятся (R-8).
func NewSettlement(placementID, advertiserOrgID, bloggerOrgID uuid.UUID, d Distribution, actor ActorType, actorUserID uuid.UUID) Transaction {
	hold := PlacementHold(placementID)
	var postings []Posting
	if d.Payout > 0 {
		postings = append(postings, Posting{Debit: hold, Credit: BloggerPayable(bloggerOrgID), Amount: d.Payout})
	}
	if d.FeeCharged > 0 {
		postings = append(postings, Posting{Debit: hold, Credit: PlatformRevenue(), Amount: d.FeeCharged})
	}
	if d.Refund > 0 {
		postings = append(postings, Posting{Debit: hold, Credit: AdvertiserBalance(advertiserOrgID), Amount: d.Refund})
	}
	return Transaction{
		Kind:           KindSettlement,
		Basis:          Basis{PlacementID: placementID},
		Actor:          actor,
		ActorUserID:    actorUserID,
		IdempotencyKey: SettlementKey(placementID),
		Postings:       postings,
	}
}

// NewPayout — выплата блогеру через депозит Рокет Ворк (P-16); комиссия сервиса — отдельной проводкой.
func NewPayout(payoutID, placementID, bloggerOrgID uuid.UUID, amount, providerFee money.Kopecks) Transaction {
	postings := []Posting{{Debit: BloggerPayable(bloggerOrgID), Credit: RocketworkDeposit(), Amount: amount}}
	if providerFee > 0 {
		postings = append(postings, Posting{Debit: PayoutFees(), Credit: RocketworkDeposit(), Amount: providerFee})
	}
	return Transaction{
		Kind:           KindPayout,
		Basis:          Basis{PayoutID: payoutID, PlacementID: placementID},
		Actor:          ActorSystem,
		IdempotencyKey: PayoutKey(payoutID),
		Postings:       postings,
	}
}

// NewLegalPayout — выплата блогеру-юрлицу переводом с расчётного счёта (R-3, A-36): одна
// проводка blogger_payable → bank, комиссии сервиса нет. Ключ тот же, что у NewPayout.
func NewLegalPayout(payoutID, placementID, bloggerOrgID uuid.UUID, amount money.Kopecks, adminID uuid.UUID, reason string) Transaction {
	return Transaction{
		Kind:           KindPayout,
		Basis:          Basis{PayoutID: payoutID, PlacementID: placementID},
		Actor:          ActorAdmin,
		ActorUserID:    adminID,
		Reason:         reason,
		IdempotencyKey: PayoutKey(payoutID),
		Postings:       []Posting{{Debit: BloggerPayable(bloggerOrgID), Credit: Bank(), Amount: amount}},
	}
}

// NewDepositTopup — администратор отразил перевод на депозит Рокет Ворк.
func NewDepositTopup(key string, amount money.Kopecks, adminID uuid.UUID, reason string) Transaction {
	return Transaction{
		Kind:           KindDepositTopup,
		Actor:          ActorAdmin,
		ActorUserID:    adminID,
		Reason:         reason,
		IdempotencyKey: OperationKey(KindDepositTopup, key),
		Postings:       []Posting{{Debit: RocketworkDeposit(), Credit: Bank(), Amount: amount}},
	}
}

// NewBalanceReturn — возврат свободного остатка рекламодателю переводом (D-26).
func NewBalanceReturn(key string, orgID uuid.UUID, amount money.Kopecks, adminID uuid.UUID, reason string) Transaction {
	return Transaction{
		Kind:           KindBalanceReturn,
		Actor:          ActorAdmin,
		ActorUserID:    adminID,
		Reason:         reason,
		IdempotencyKey: OperationKey(KindBalanceReturn, key),
		Postings:       []Posting{{Debit: AdvertiserBalance(orgID), Credit: Bank(), Amount: amount}},
	}
}

// NewManual — ручная корректировка с обязательным основанием (S-19).
func NewManual(key string, postings []Posting, adminID uuid.UUID, reason string) Transaction {
	return Transaction{
		Kind:           KindManual,
		Actor:          ActorAdmin,
		ActorUserID:    adminID,
		Reason:         reason,
		IdempotencyKey: OperationKey(KindManual, key),
		Postings:       postings,
	}
}
