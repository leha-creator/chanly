package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var (
	f124 = Format{ID: "f_1_24", TopHours: 1, DurationHours: 24}
	f248 = Format{ID: "f_2_48", TopHours: 2, DurationHours: 48}
)

func testOffer(owner uuid.UUID) ChannelOffer {
	return ChannelOffer{
		Ref: ChannelRef{ID: uuid.New(), Title: "Канал"}, OwnerOrgID: &owner, Active: true, Visible: true,
		Prices: []FormatPrice{
			{Format: f124, Amount: 1_000_000, Enabled: true, FormatActive: true},
			{Format: f248, Amount: 1_800_000, Enabled: false, FormatActive: true},
		},
	}
}

func TestTotalAmount(t *testing.T) {
	// 06 §3: 10 000,00 ₽ при 1500 б. п. — комиссия 1 500,00, итог 11 500,00.
	if got := TotalAmount(1_000_000, 1500); got != 1_150_000 {
		t.Errorf("total %d", got)
	}
	// Округление вниз: 10 000,01 ₽ × 15 % = 1 500,0015 → 1 500,00.
	if got := TotalAmount(1_000_001, 1500); got != 1_150_001 {
		t.Errorf("odd kopecks total %d", got)
	}
	if got := TotalAmount(10_007, 1500); got != 10_007+1_501 {
		t.Errorf("small total %d", got)
	}
}

func TestCheckAdd(t *testing.T) {
	org, blogger := uuid.New(), uuid.New()
	offer := testOffer(blogger)
	r := CartRules{MaxItems: 50}
	if price, err := CheckAdd(org, &offer, "f_1_24", false, 0, r); err != nil || price.Amount != 1_000_000 {
		t.Fatalf("add: %v %+v", err, price)
	}
	hidden := testOffer(blogger)
	hidden.Visible = false
	own := testOffer(org)
	tests := []struct {
		name   string
		offer  *ChannelOffer
		format string
		inCart bool
		count  int
		want   error
	}{
		{"no channel", nil, "f_1_24", false, 0, ErrChannelNotInCatalog},
		{"not in catalog", &hidden, "f_1_24", false, 0, ErrChannelNotInCatalog},
		{"already in cart", &offer, "f_1_24", true, 0, ErrAlreadyInCart},
		{"own channel", &own, "f_1_24", false, 0, ErrOwnChannel},
		{"price disabled", &offer, "f_2_48", false, 0, ErrFormatUnavailable},
		{"unknown format", &offer, "f_9_99", false, 0, ErrFormatUnavailable},
		{"51st item", &offer, "f_1_24", false, 50, ErrCartFull},
	}
	for _, tc := range tests {
		if _, err := CheckAdd(org, tc.offer, tc.format, tc.inCart, tc.count, r); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := CheckFormat(&offer, "f_2_48"); !errors.Is(err, ErrFormatUnavailable) {
		t.Errorf("change to disabled format: %v", err)
	}
}

func TestEvaluateCart(t *testing.T) {
	blogger := uuid.New()
	ok, changed, disabled, suspended := testOffer(blogger), testOffer(blogger), testOffer(blogger), testOffer(blogger)
	changed.Prices[0].Amount = 1_200_000
	disabled.Prices[0].Enabled = false
	suspended.Active, suspended.Visible = false, false
	item := func(o ChannelOffer) CartItem {
		return CartItem{ID: uuid.New(), ChannelID: o.Ref.ID, FormatID: "f_1_24", ShownTotal: 1_150_000}
	}
	gone := CartItem{ID: uuid.New(), ChannelID: uuid.New(), FormatID: "f_1_24", ShownTotal: 1_150_000}
	items := []CartItem{item(ok), item(changed), item(disabled), item(suspended), gone}
	offers := map[uuid.UUID]ChannelOffer{ok.Ref.ID: ok, changed.Ref.ID: changed, disabled.Ref.ID: disabled, suspended.Ref.ID: suspended}
	lines := EvaluateCart(items, offers, 1500)
	type want struct {
		current   *money.Kopecks
		changed   bool
		available bool
	}
	k := func(v money.Kopecks) *money.Kopecks { return &v }
	wants := []want{{k(1_150_000), false, true}, {k(1_380_000), true, true}, {nil, false, false}, {k(1_150_000), false, false}, {nil, false, false}}
	for i, w := range wants {
		l := lines[i]
		if (l.Current == nil) != (w.current == nil) || (l.Current != nil && *l.Current != *w.current) ||
			l.PriceChanged != w.changed || l.Available != w.available {
			t.Errorf("line %d: %+v", i, l)
		}
	}
	if lines[0].Channel.Title != "Канал" || lines[4].Channel.ID != gone.ChannelID {
		t.Errorf("channel refs: %+v %+v", lines[0].Channel, lines[4].Channel)
	}
	if got := CartTotal(lines); got != 1_150_000+1_380_000 {
		t.Errorf("cart total %d", got)
	}
}
