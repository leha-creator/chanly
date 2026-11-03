package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var (
	org       = uuid.MustParse("01900000-0000-7000-8000-000000000001")
	blogger   = uuid.MustParse("01900000-0000-7000-8000-000000000002")
	placement = uuid.MustParse("01900000-0000-7000-8000-000000000003")
	version   = uuid.MustParse("01900000-0000-7000-8000-000000000004")
	bankTx    = uuid.MustParse("01900000-0000-7000-8000-000000000005")
	payoutID  = uuid.MustParse("01900000-0000-7000-8000-000000000006")
	admin     = uuid.MustParse("01900000-0000-7000-8000-000000000007")
)

// Каждая строка 06 §2.3 собирается построителем и проходит правило вида.
func TestBuildersAreValid(t *testing.T) {
	cases := map[string]Transaction{
		"deposit":         NewDeposit(bankTx, uuid.Nil, org, 2000000, ActorSystem, uuid.Nil),
		"hold":            NewHold(placement, version, org, 1150000),
		"hold_release":    NewHoldRelease(placement, version, org, 1150000),
		"settlement":      NewSettlement(placement, org, blogger, Distribution{Payout: 750000, FeeCharged: 112500, Refund: 287500}, ActorSystem, uuid.Nil),
		"settlement_full": NewSettlement(placement, org, blogger, Distribution{Payout: 1000000, FeeCharged: 150000}, ActorSystem, uuid.Nil),
		"payout":          NewPayout(payoutID, placement, blogger, 750000, 7500),
		"payout_no_fee":   NewPayout(payoutID, placement, blogger, 750000, 0),
		"payout_legal":    NewLegalPayout(payoutID, placement, blogger, 750000, admin, "п/п 12"),
		"deposit_topup":   NewDepositTopup("k1", 5000000, admin, "перевод на депозит"),
		"balance_return":  NewBalanceReturn("k2", org, 100000, admin, "обращение 15"),
		"manual":          NewManual("k3", []Posting{{Debit: PlatformRevenue(), Credit: AdvertiserBalance(org), Amount: 100}}, admin, "исправление"),
	}
	for name, tx := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tx.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	swapped := NewHold(placement, version, org, 100)
	swapped.Postings[0].Debit, swapped.Postings[0].Credit = swapped.Postings[0].Credit, swapped.Postings[0].Debit

	otherPlacement := NewHold(placement, version, org, 100)
	otherPlacement.Basis.PlacementID = uuid.MustParse("01900000-0000-7000-8000-0000000000ff")

	noReason := NewManual("k", []Posting{{Debit: Bank(), Credit: AdvertiserBalance(org), Amount: 1}}, admin, " ")
	byAdvertiser := NewBalanceReturn("k", org, 100, admin, "x")
	byAdvertiser.Actor = ActorAdvertiser

	settlementWrongOrder := NewSettlement(placement, org, blogger, Distribution{Payout: 1, FeeCharged: 1, Refund: 1}, ActorSystem, uuid.Nil)
	settlementWrongOrder.Postings[0], settlementWrongOrder.Postings[2] = settlementWrongOrder.Postings[2], settlementWrongOrder.Postings[0]

	settlementTwice := NewSettlement(placement, org, blogger, Distribution{Payout: 1}, ActorSystem, uuid.Nil)
	settlementTwice.Postings = append(settlementTwice.Postings, settlementTwice.Postings[0])

	depositNoBasis := NewDeposit(uuid.Nil, uuid.Nil, org, 1, ActorSystem, uuid.Nil)

	zero := NewHold(placement, version, org, 0)
	same := NewManual("k", []Posting{{Debit: Bank(), Credit: Bank(), Amount: 1}}, admin, "x")
	badScope := NewManual("k", []Posting{{Debit: Bank(), Credit: AccountRef{Kind: AccountAdvertiserBalance}, Amount: 1}}, admin, "x")
	noKey := NewHold(placement, version, org, 1)
	noKey.IdempotencyKey = ""
	empty := NewSettlement(placement, org, blogger, Distribution{}, ActorSystem, uuid.Nil)
	legalWithFee := NewLegalPayout(payoutID, placement, blogger, 100, admin, "п/п 1")
	legalWithFee.Postings = append(legalWithFee.Postings, Posting{Debit: PayoutFees(), Credit: Bank(), Amount: 1})
	legalBySystem := NewLegalPayout(payoutID, placement, blogger, 100, admin, "п/п 1")
	legalBySystem.Actor = ActorSystem
	legalNoReason := NewLegalPayout(payoutID, placement, blogger, 100, admin, "")
	feeToBank := NewPayout(payoutID, placement, blogger, 100, 1)
	feeToBank.Postings[1].Credit = Bank()

	cases := map[string]Transaction{
		"swapped hold":           swapped,
		"hold of other place":    otherPlacement,
		"manual without reason":  noReason,
		"return by advertiser":   byAdvertiser,
		"settlement wrong order": settlementWrongOrder,
		"settlement twice":       settlementTwice,
		"deposit without basis":  depositNoBasis,
		"zero amount":            zero,
		"same accounts":          same,
		"scope mismatch":         badScope,
		"empty key":              noKey,
		"no postings":            empty,
		"legal payout with fee":  legalWithFee,
		"legal payout by system": legalBySystem,
		"legal payout no reason": legalNoReason,
		"payout fee to bank":     feeToBank,
	}
	for name, tx := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tx.Validate(); !errors.Is(err, ErrInvalidPosting) {
				t.Fatalf("Validate() = %v, want ErrInvalidPosting", err)
			}
		})
	}
}

func TestEntriesHash(t *testing.T) {
	a := NewHold(placement, version, org, 1150000)
	b := NewHold(placement, version, org, 1150000)
	b.ActorUserID = admin // автор и основание в отпечаток не входят
	b.Reason = "x"
	if a.EntriesHash() != b.EntriesHash() {
		t.Fatal("hash depends on actor or reason")
	}
	c := NewHold(placement, version, org, 1150001)
	if a.EntriesHash() == c.EntriesHash() {
		t.Fatal("hash does not depend on amount")
	}
	d := NewHoldRelease(placement, version, org, 1150000)
	if a.EntriesHash() == d.EntriesHash() {
		t.Fatal("hash does not depend on kind and accounts")
	}
}

func TestApplyPostings(t *testing.T) {
	bal := AdvertiserBalance(org)
	start := map[AccountRef]money.Kopecks{bal: 1000000}

	// Пассив в минус — отказ с недостающей суммой.
	_, err := ApplyPostings(start, NewHold(placement, version, org, 1150000).Postings)
	var neg *NegativeLiabilityError
	if !errors.As(err, &neg) || neg.Missing != 150000 || neg.Account != bal {
		t.Fatalf("ApplyPostings = %v, want missing 150000 on advertiser balance", err)
	}
	if start[bal] != 1000000 {
		t.Fatal("source map changed")
	}

	// Актив в минус — разрешено (возврат остатка больше поступлений банка).
	out, err := ApplyPostings(start, NewBalanceReturn("k", org, 400000, admin, "x").Postings)
	if err != nil {
		t.Fatal(err)
	}
	if out[Bank()] != -400000 || out[bal] != 600000 {
		t.Fatalf("balances = %v", out)
	}
}

// Пример 06 §2.3: 11 500 → 7 500 / 1 125 / 2 875, выплата с комиссией 75. После каждого шага
// сумма активов равна сумме пассивов.
func TestExampleConverges(t *testing.T) {
	steps := []Transaction{
		NewDeposit(bankTx, uuid.Nil, org, 2000000, ActorSystem, uuid.Nil),
		NewHold(placement, version, org, 1150000),
		NewSettlement(placement, org, blogger, Distribution{Payout: 750000, FeeCharged: 112500, Refund: 287500}, ActorSystem, uuid.Nil),
		NewPayout(payoutID, placement, blogger, 750000, 7500),
	}
	bal := map[AccountRef]money.Kopecks{}
	for _, s := range steps {
		var err error
		if bal, err = ApplyPostings(bal, s.Postings); err != nil {
			t.Fatalf("%s: %v", s.Kind, err)
		}
		var assets, liabilities money.Kopecks
		for ref, v := range bal {
			if ref.Kind.Side() == Asset {
				assets += v
			} else {
				liabilities += v
			}
		}
		if assets != liabilities {
			t.Fatalf("after %s: assets %d != liabilities %d", s.Kind, assets, liabilities)
		}
	}
	want := map[AccountRef]money.Kopecks{
		Bank(): 2000000, AdvertiserBalance(org): 850000 + 287500, PlacementHold(placement): 0,
		BloggerPayable(blogger): 0, PlatformRevenue(): 112500, RocketworkDeposit(): -757500, PayoutFees(): 7500,
	}
	for ref, v := range want {
		if bal[ref] != v {
			t.Errorf("%s = %d, want %d", ref, bal[ref], v)
		}
	}
}

func TestShortfall(t *testing.T) {
	if Shortfall(100, 100) != 0 || Shortfall(100, 150) != 50 || Shortfall(0, 1) != 1 {
		t.Fatal("Shortfall")
	}
}
