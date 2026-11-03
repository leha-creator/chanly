package domain

import (
	"testing"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Формулы 03 §7 на граничных долях и суммах с остатком при округлении (07 §9).
func TestDistribute(t *testing.T) {
	cases := []struct {
		name                       string
		blogger, fee               money.Kopecks
		share                      int
		payout, feeCharged, refund money.Kopecks
	}{
		{"example 03 §7", 1000000, 150000, 7500, 750000, 112500, 287500},
		{"full payout", 1000000, 150000, 10000, 1000000, 150000, 0},
		{"full refund", 1000000, 150000, 0, 0, 0, 1150000},
		{"share 1", 1000000, 150000, 1, 100, 15, 1149885},
		{"share 9999", 1000000, 150000, 9999, 999900, 149985, 115},
		{"half", 1000000, 150000, 5000, 500000, 75000, 575000},
		{"rounding goes to advertiser", 1000001, 150000, 3333, 333300, 49995, 766706},
		{"odd kopecks", 333, 49, 5000, 166, 24, 192},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Distribute(c.blogger, c.fee, c.blogger+c.fee, c.share)
			if err != nil {
				t.Fatal(err)
			}
			if d.PayoutAmount != c.payout || d.FeeCharged != c.feeCharged || d.RefundAmount != c.refund {
				t.Fatalf("Distribute = %+v, want %d/%d/%d", d, c.payout, c.feeCharged, c.refund)
			}
			// I-12.
			if d.PayoutAmount+d.FeeCharged+d.RefundAmount != c.blogger+c.fee {
				t.Fatal("I-12 violated")
			}
		})
	}
}

func TestDistributeRejects(t *testing.T) {
	if _, err := Distribute(100, 15, 115, -1); err == nil {
		t.Error("negative share accepted")
	}
	if _, err := Distribute(100, 15, 115, 10001); err == nil {
		t.Error("share above 10000 accepted")
	}
	if _, err := Distribute(100, 15, 116, 5000); err == nil {
		t.Error("inconsistent total accepted")
	}
}
