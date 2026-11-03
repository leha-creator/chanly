package domain

import (
	"fmt"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Distribution — распределение удержания при переходе в accepted или refunded (03 §7).
type Distribution struct {
	PerformanceShare int
	PayoutAmount     money.Kopecks
	FeeCharged       money.Kopecks
	RefundAmount     money.Kopecks
}

// Distribute — суммы по доле исполнения share в базисных пунктах (03 §7): выплата и комиссия
// уменьшаются в той же доле с округлением вниз, остаток от округления уходит рекламодателю.
// Результат удовлетворяет I-12.
func Distribute(bloggerAmount, feeAmount, totalAmount money.Kopecks, share int) (Distribution, error) {
	if share < 0 || share > money.FullBP {
		return Distribution{}, fmt.Errorf("placement: performance share %d is out of range", share)
	}
	if bloggerAmount < 0 || feeAmount < 0 || totalAmount != bloggerAmount+feeAmount {
		return Distribution{}, fmt.Errorf("placement: amounts do not add up: %d + %d != %d", bloggerAmount, feeAmount, totalAmount)
	}
	d := Distribution{
		PerformanceShare: share,
		PayoutAmount:     bloggerAmount.ShareBP(share),
		FeeCharged:       feeAmount.ShareBP(share),
	}
	d.RefundAmount = totalAmount - d.PayoutAmount - d.FeeCharged
	return d, nil
}
