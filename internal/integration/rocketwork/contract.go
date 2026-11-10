package rocketwork

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// rublesToKopecks parses decimal JSON numbers/strings without floating point or
// rounding. Fractional kopecks and overflow fail instead of changing the amount.
func rublesToKopecks(raw string) (int64, error) {
	if raw == "" || len(raw) > 64 || strings.ContainsAny(raw, "/ \t\r\n") || !json.Valid([]byte(raw)) {
		return 0, fmt.Errorf("%w: invalid monetary value", ErrFormat)
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exp, err := strconv.Atoi(raw[i+1:])
		if err != nil || exp < -20 || exp > 20 {
			return 0, fmt.Errorf("%w: monetary exponent", ErrFormat)
		}
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, fmt.Errorf("%w: invalid monetary value", ErrFormat)
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, fmt.Errorf("%w: monetary precision or overflow", ErrFormat)
	}
	return r.Num().Int64(), nil
}

func (d taskDTO) payment() (Payment, error) {
	if d.ID <= 0 || d.Type != "OneTime" || d.Status == "" {
		return Payment{}, fmt.Errorf("%w: invalid payout task", ErrFormat)
	}
	p := Payment{ID: strconv.FormatInt(d.ID, 10), ExternalID: d.ExternalID, Status: PaymentSent, RawStatus: d.Status}
	amount, err := requiredMoney(d.AmountGross)
	if err != nil {
		return Payment{}, err
	}
	p.AmountKopecks = amount
	if d.ExecutorAmountGross != nil {
		executorAmount, err := requiredMoney(d.ExecutorAmountGross)
		if err != nil {
			return Payment{}, err
		}
		p.ExecutorAmountKopecks = &executorAmount
	}
	switch d.Status {
	case "draft", "inwork", "accepting", "completed", "paying", "error":
		// error belongs to an existing task; it never authorizes a new payment.
		p.Known = true
	case "paid":
		if d.Commission == nil {
			return Payment{}, fmt.Errorf("%w: missing final commission", ErrFormat)
		}
		fee, err := rublesToKopecks(*d.Commission)
		if err != nil || fee < 0 {
			return Payment{}, fmt.Errorf("%w: invalid final commission", ErrFormat)
		}
		p.Status, p.Known, p.FeeKopecks, p.ReceiptURL = PaymentSucceeded, true, fee, d.ReceiptURI
	case "canceled":
		p.Status, p.Known, p.FailureCode = PaymentFailed, true, FailureProviderError
	}
	return p, nil
}
