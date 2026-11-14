package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAtomicProfileAndPrices(t *testing.T) {
	for _, status := range []Status{StatusActive, StatusSuspended, StatusModeration, StatusPendingRights, StatusRejected, StatusBlocked, StatusDisconnected, StatusUnclaimed} {
		t.Run(string(status), func(t *testing.T) {
			c := priced(status)
			before := *c
			description := "new description"
			prices := []Price{{FormatID: "f_2_48", Amount: 400000, IsEnabled: true}}
			change, err := c.UpdateProfileWithPrices(ProfileUpdate{Description: &description, Prices: &prices}, formats, rkn, t0.Add(time.Minute), 0)
			if status == StatusBlocked || status == StatusDisconnected || status == StatusUnclaimed {
				if !errors.Is(err, ErrNotEditable) || !reflect.DeepEqual(*c, before) {
					t.Fatalf("non-editable mutated: %v", err)
				}
				return
			}
			if err != nil || c.Description != description || len(c.Prices) != 1 || c.Prices[0].FormatID != "f_2_48" || change.From != status || change.To != c.Status {
				t.Fatalf("save: %+v %v", change, err)
			}
		})
	}
}

func TestAtomicPricesCannotBypassOriginalOrFinalActive(t *testing.T) {
	for _, status := range []Status{StatusActive, StatusSuspended} {
		c := priced(status)
		before := *c
		empty := []Price{}
		number := "new-rkn"
		p := ProfileUpdate{Prices: &empty}
		if status == StatusActive {
			c.SubscribersCount, c.RknStatus = rkn, RknConfirmed
			before = *c
			p.RknNumber = &number // would suspend before a sequential price call
		}
		_, err := c.UpdateProfileWithPrices(p, formats, rkn, t0.Add(time.Hour), 0)
		if !errors.Is(err, ErrActiveRequiresPrice) || !reflect.DeepEqual(*c, before) {
			t.Fatalf("%s bypass or mutation: %v", status, err)
		}
	}
}

func TestAtomicInvalidPricesAndOmission(t *testing.T) {
	c := priced(StatusModeration)
	before := *c
	description := "new"
	bad := []Price{{FormatID: "f_1_24", Amount: 1, IsEnabled: true}}
	if _, err := c.UpdateProfileWithPrices(ProfileUpdate{Description: &description, Prices: &bad}, formats, rkn, t0, 0); !errors.Is(err, ErrPriceOutOfRange) || !reflect.DeepEqual(*c, before) {
		t.Fatalf("invalid prices mutated profile: %v", err)
	}
	if _, err := c.UpdateProfile(ProfileUpdate{Description: &description}, rkn, t0); err != nil || !reflect.DeepEqual(c.Prices, before.Prices) {
		t.Fatalf("omitted prices: %v", err)
	}
	empty := []Price{}
	if _, err := c.UpdateProfileWithPrices(ProfileUpdate{Prices: &empty}, formats, rkn, t0, 0); err != nil || len(c.Prices) != 0 {
		t.Fatalf("empty moderation prices: %v", err)
	}
}
