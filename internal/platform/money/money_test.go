package money_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestShareBP(t *testing.T) {
	for _, tc := range []struct {
		amount money.Kopecks
		bp     int
		want   money.Kopecks
	}{
		{10000, 1500, 1500},
		{10001, 1500, 1500}, // 1500,15 → вниз
		{1_000_000_000, 1500, 150_000_000},
		{10001, 0, 0},
		{10001, 10000, 10001},
		{1, 9999, 0},
	} {
		if got := tc.amount.ShareBP(tc.bp); got != tc.want {
			t.Errorf("%d × %d bp = %d, want %d", tc.amount, tc.bp, got, tc.want)
		}
	}
}
