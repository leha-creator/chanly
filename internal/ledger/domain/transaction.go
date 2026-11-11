package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Kind — вид транзакции (06 §2.3, ledger_transaction.kind).
type Kind string

const (
	KindDeposit       Kind = "deposit"
	KindHold          Kind = "hold"
	KindHoldRelease   Kind = "hold_release"
	KindSettlement    Kind = "settlement"
	KindPayout        Kind = "payout"
	KindDepositTopup  Kind = "deposit_topup"
	KindBalanceReturn Kind = "balance_return"
	KindManual        Kind = "manual"
	KindDiagnostic    Kind = "rw_diagnostic"
)

// Kinds — все виды в порядке 06 §2.3.
var Kinds = []Kind{
	KindDeposit, KindHold, KindHoldRelease, KindSettlement, KindPayout, KindDepositTopup, KindBalanceReturn, KindManual,
	KindDiagnostic,
}

// ActorType — кто запустил движение (ledger_transaction.actor_type).
type ActorType string

const (
	ActorSystem     ActorType = "system"
	ActorAdmin      ActorType = "admin"
	ActorAdvertiser ActorType = "advertiser"
)

// Basis — основание транзакции; заполняется подходящее (04 §7).
type Basis struct {
	PlacementID       uuid.UUID
	InvoiceID         uuid.UUID
	BankTransactionID uuid.UUID
	PayoutID          uuid.UUID
}

// Posting — проводка: дебет одного счёта, кредит другого, сумма больше нуля (06 §2.2 п. 1).
type Posting struct {
	Debit  AccountRef
	Credit AccountRef
	Amount money.Kopecks
}

// Transaction — одно движение денег: одна или несколько проводок под одним ключом.
type Transaction struct {
	Kind           Kind
	Basis          Basis
	Actor          ActorType
	ActorUserID    uuid.UUID
	Reason         string
	IdempotencyKey string
	Postings       []Posting
}

// MaxKeyLength — предел ключа идемпотентности (ledger_transaction.idempotency_key).
const MaxKeyLength = 200

// MaxReasonLength — предел основания.
const MaxReasonLength = 1000

// Validate — состав транзакции по правилу вида (R-4), обязательные основание и причина.
func (t Transaction) Validate() error {
	if t.IdempotencyKey == "" || len(t.IdempotencyKey) > MaxKeyLength {
		return fmt.Errorf("%w: idempotency key length", ErrInvalidPosting)
	}
	if len([]rune(t.Reason)) > MaxReasonLength {
		return fmt.Errorf("%w: reason is too long", ErrInvalidPosting)
	}
	switch t.Actor {
	case ActorSystem, ActorAdmin, ActorAdvertiser:
	default:
		return fmt.Errorf("%w: unknown actor %q", ErrInvalidPosting, t.Actor)
	}
	if len(t.Postings) == 0 {
		return fmt.Errorf("%w: %s without postings", ErrInvalidPosting, t.Kind)
	}
	for i, p := range t.Postings {
		if err := p.Debit.Validate(); err != nil {
			return fmt.Errorf("posting %d debit: %w", i, err)
		}
		if err := p.Credit.Validate(); err != nil {
			return fmt.Errorf("posting %d credit: %w", i, err)
		}
		if p.Debit == p.Credit {
			return fmt.Errorf("%w: posting %d: debit and credit are the same account", ErrInvalidPosting, i)
		}
		if p.Amount <= 0 {
			return fmt.Errorf("%w: posting %d: amount must be positive", ErrInvalidPosting, i)
		}
	}
	rule, ok := rules[t.Kind]
	if !ok {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidPosting, t.Kind)
	}
	return rule(t)
}

// rules — допустимые проводки каждого вида (06 §2.3, R-4).
var rules = map[Kind]func(Transaction) error{
	KindDiagnostic: func(t Transaction) error {
		if t.Actor != ActorSystem || !strings.HasPrefix(t.IdempotencyKey, "rw_diagnostic:") || t.Basis != (Basis{}) || len(t.Postings) > 2 {
			return ErrInvalidPosting
		}
		if _, err := uuid.Parse(strings.TrimPrefix(t.IdempotencyKey, "rw_diagnostic:")); err != nil {
			return ErrInvalidPosting
		}
		if t.Postings[0].Debit != DiagnosticExpense() || t.Postings[0].Credit != RocketworkDeposit() {
			return ErrInvalidPosting
		}
		if len(t.Postings) == 2 && (t.Postings[1].Debit != PayoutFees() || t.Postings[1].Credit != RocketworkDeposit()) {
			return ErrInvalidPosting
		}
		return nil
	},
	KindDeposit: func(t Transaction) error {
		if t.Basis.BankTransactionID == uuid.Nil {
			return fmt.Errorf("%w: deposit without bank transaction", ErrInvalidPosting)
		}
		return single(t, AccountBank, AccountAdvertiserBalance)
	},
	KindHold: func(t Transaction) error {
		if err := single(t, AccountAdvertiserBalance, AccountPlacementHold); err != nil {
			return err
		}
		return samePlacement(t, t.Postings[0].Credit)
	},
	KindHoldRelease: func(t Transaction) error {
		if err := single(t, AccountPlacementHold, AccountAdvertiserBalance); err != nil {
			return err
		}
		return samePlacement(t, t.Postings[0].Debit)
	},
	KindSettlement: func(t Transaction) error {
		// Порядок фиксирован: блогеру, комиссия, возврат; нулевые суммы не проводятся (R-8).
		order := []AccountKind{AccountBloggerPayable, AccountPlatformRevenue, AccountAdvertiserBalance}
		next := 0
		for i, p := range t.Postings {
			if p.Debit.Kind != AccountPlacementHold {
				return fmt.Errorf("%w: settlement posting %d must debit placement_hold", ErrInvalidPosting, i)
			}
			if err := samePlacement(t, p.Debit); err != nil {
				return err
			}
			for next < len(order) && order[next] != p.Credit.Kind {
				next++
			}
			if next == len(order) {
				return fmt.Errorf("%w: settlement posting %d: unexpected credit %s", ErrInvalidPosting, i, p.Credit.Kind)
			}
			next++
		}
		return nil
	},
	KindPayout: func(t Transaction) error {
		if t.Basis.PayoutID == uuid.Nil {
			return fmt.Errorf("%w: payout without payout id", ErrInvalidPosting)
		}
		if len(t.Postings) > 2 {
			return fmt.Errorf("%w: payout has %d postings", ErrInvalidPosting, len(t.Postings))
		}
		// Второй вариант (R-3, A-36): юрлицу переводом из bank, одна проводка, только администратор.
		if p := t.Postings[0]; p.Credit.Kind == AccountBank {
			if p.Debit.Kind != AccountBloggerPayable || len(t.Postings) != 1 {
				return fmt.Errorf("%w: bank payout must be a single blogger_payable to bank posting", ErrInvalidPosting)
			}
			if t.Actor != ActorAdmin || strings.TrimSpace(t.Reason) == "" {
				return fmt.Errorf("%w: bank payout needs an admin and a reason", ErrInvalidPosting)
			}
			return nil
		}
		if p := t.Postings[0]; p.Debit.Kind != AccountBloggerPayable || p.Credit.Kind != AccountRocketworkDeposit {
			return fmt.Errorf("%w: payout must move blogger_payable to rocketwork_deposit", ErrInvalidPosting)
		}
		if len(t.Postings) == 2 {
			if p := t.Postings[1]; p.Debit.Kind != AccountPayoutFees || p.Credit.Kind != AccountRocketworkDeposit {
				return fmt.Errorf("%w: payout fee must move payout_fees to rocketwork_deposit", ErrInvalidPosting)
			}
		}
		return nil
	},
	KindDepositTopup: func(t Transaction) error {
		if t.Actor != ActorAdmin {
			return fmt.Errorf("%w: deposit_topup is an admin operation", ErrInvalidPosting)
		}
		return single(t, AccountRocketworkDeposit, AccountBank)
	},
	KindBalanceReturn: func(t Transaction) error {
		if t.Actor != ActorAdmin || strings.TrimSpace(t.Reason) == "" {
			return fmt.Errorf("%w: balance_return needs an admin and a reason", ErrInvalidPosting)
		}
		return single(t, AccountAdvertiserBalance, AccountBank)
	},
	KindManual: func(t Transaction) error {
		if t.Actor != ActorAdmin || strings.TrimSpace(t.Reason) == "" {
			return fmt.Errorf("%w: manual needs an admin and a reason", ErrInvalidPosting)
		}
		return nil
	},
}

func single(t Transaction, debit, credit AccountKind) error {
	if len(t.Postings) != 1 {
		return fmt.Errorf("%w: %s must have one posting, got %d", ErrInvalidPosting, t.Kind, len(t.Postings))
	}
	if p := t.Postings[0]; p.Debit.Kind != debit || p.Credit.Kind != credit {
		return fmt.Errorf("%w: %s must move %s to %s, got %s to %s",
			ErrInvalidPosting, t.Kind, debit, credit, p.Debit.Kind, p.Credit.Kind)
	}
	return nil
}

func samePlacement(t Transaction, hold AccountRef) error {
	if t.Basis.PlacementID == uuid.Nil || hold.PlacementID != t.Basis.PlacementID {
		return fmt.Errorf("%w: %s: placement_hold does not match basis placement", ErrInvalidPosting, t.Kind)
	}
	return nil
}

// EntriesHash — отпечаток состава (R-2): вид и проводки по порядку. Не зависит от
// основания, автора и причины.
func (t Transaction) EntriesHash() string {
	var b strings.Builder
	b.WriteString(string(t.Kind))
	for _, p := range t.Postings {
		b.WriteByte('\n')
		b.WriteString(p.Debit.String())
		b.WriteByte('|')
		b.WriteString(p.Credit.String())
		b.WriteByte('|')
		b.WriteString(strconv.FormatInt(int64(p.Amount), 10))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Total — сумма проводок.
func (t Transaction) Total() money.Kopecks {
	var total money.Kopecks
	for _, p := range t.Postings {
		total += p.Amount
	}
	return total
}

// Accounts — счета транзакции без повторов в порядке появления.
func (t Transaction) Accounts() []AccountRef {
	var out []AccountRef
	seen := map[AccountRef]bool{}
	for _, p := range t.Postings {
		for _, r := range []AccountRef{p.Debit, p.Credit} {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}
