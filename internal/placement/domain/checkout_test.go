package domain

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

func readyCampaign() CampaignSnapshot {
	df, dt := date(2026, 10, 20), date(2026, 10, 22)
	tf, tt := TimeOfDay(9*60), TimeOfDay(21*60)
	return CampaignSnapshot{ID: uuid.New(), Status: CampaignDraft, DateFrom: &df, DateTo: &dt, TimeFrom: &tf, TimeTo: &tt}
}

func TestCheckCampaign(t *testing.T) {
	now := mskTime(2026, 10, 20, 8, 0)
	w, err := CheckCampaign(readyCampaign(), now, DefaultTiming)
	if err != nil || w != testWindow() {
		t.Fatalf("ready campaign: %v %+v", err, w)
	}
	// D-20: оплата в день начала периода допустима.
	if _, err := CheckCampaign(readyCampaign(), mskTime(2026, 10, 20, 23, 30), DefaultTiming); err != nil {
		t.Fatalf("same day: %v", err)
	}
	for _, field := range []string{"date_from", "date_to", "time_from", "time_to"} {
		c := readyCampaign()
		switch field {
		case "date_from":
			c.DateFrom = nil
		case "date_to":
			c.DateTo = nil
		case "time_from":
			c.TimeFrom = nil
		case "time_to":
			c.TimeTo = nil
		}
		var ie *IncompleteError
		if _, err := CheckCampaign(c, now, DefaultTiming); !errors.As(err, &ie) || !slices.Equal(ie.MissingFields, []string{field}) {
			t.Errorf("missing %s: %v", field, err)
		}
	}
	var ie *IncompleteError
	if _, err := CheckCampaign(readyCampaign(), mskTime(2026, 10, 21, 10, 0), DefaultTiming); !errors.As(err, &ie) ||
		!slices.Equal(ie.MissingFields, []string{"date_from"}) {
		t.Errorf("period in the past: %v", err)
	}
	// До конца окна ровно 1 ч — нельзя.
	c := readyCampaign()
	c.DateFrom = c.DateTo
	if _, err := CheckCampaign(c, mskTime(2026, 10, 22, 23, 0), DefaultTiming); !errors.As(err, &ie) ||
		!slices.Equal(ie.MissingFields, []string{"date_to"}) {
		t.Errorf("window ends within guard: %v", err)
	}
	c = readyCampaign()
	c.Status = CampaignOrdered
	if _, err := CheckCampaign(c, now, DefaultTiming); !errors.Is(err, ErrCampaignNotDraft) {
		t.Errorf("ordered campaign: %v", err)
	}
}

func TestCheckCartForCheckout(t *testing.T) {
	var ce *CartChangedError
	if err := CheckCartForCheckout(nil); !errors.As(err, &ce) || len(ce.Items) != 0 {
		t.Fatalf("empty cart: %v", err)
	}
	total := money.Kopecks(1_150_000)
	good := CartLine{Item: CartItem{ID: uuid.New()}, Current: &total, Available: true}
	changed := CartLine{Item: CartItem{ID: uuid.New()}, Current: &total, Available: true, PriceChanged: true}
	disabled := CartLine{Item: CartItem{ID: uuid.New()}}
	if err := CheckCartForCheckout([]CartLine{good}); err != nil {
		t.Fatalf("good cart: %v", err)
	}
	err := CheckCartForCheckout([]CartLine{good, changed, disabled})
	if !errors.As(err, &ce) || !slices.Equal(ce.Items, []uuid.UUID{changed.Item.ID, disabled.Item.ID}) {
		t.Fatalf("changed cart: %v", err)
	}
}

func TestNewPlacement(t *testing.T) {
	blogger := uuid.New()
	offer := testOffer(blogger)
	offer.Prices[0].Amount = 1_000_001
	price, _ := offer.Price("f_1_24")
	w := testWindow()
	now := mskTime(2026, 10, 20, 8, 0)
	user, version := uuid.New(), uuid.New()
	p, c, err := NewPlacement(uuid.New(), uuid.New(), offer, price, w, version, 1500, user, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusCreated || p.amounts() != [3]money.Kopecks{1_000_001, 150_000, 1_150_001} || p.BloggerOrgID != blogger ||
		p.TopHours != 1 || p.DurationHours != 24 || p.Window() != w || p.PostVersionID != version || p.FeeRateBP != 1500 {
		t.Fatalf("placement %+v", p)
	}
	if c.Code != P01Create || c.From != StatusNone || c.Actor != ActorAdvertiser || *c.UserID != user ||
		len(c.Jobs) != 1 || c.Jobs[0].DedupeKey != TimerKey(p.ID, TimerWindow) || !c.Jobs[0].RunAt.Equal(w.End.Add(-DefaultTiming.WindowGuard)) {
		t.Fatalf("P-01 change %+v", c)
	}
	if OrderTotal([]Placement{p, p}) != 2_300_002 {
		t.Fatal("order total")
	}
	offer.OwnerOrgID = nil
	if _, _, err := NewPlacement(uuid.New(), uuid.New(), offer, price, w, version, 1500, user, now, DefaultTiming); err == nil {
		t.Fatal("channel without owner")
	}
}

// amounts — суммы размещения для сравнения в тестах.
func (p Placement) amounts() [3]money.Kopecks {
	return [3]money.Kopecks{p.BloggerAmount, p.FeeAmount, p.TotalAmount}
}
