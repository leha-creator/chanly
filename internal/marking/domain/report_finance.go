package domain

import (
	"fmt"
	"math/big"
)

// TaxTotal is an immutable report snapshot in kopecks. Empty rate means a
// mixed-rate total; missing input is distinguished by Known=false.
type TaxTotal struct {
	Gross int64  `json:"gross"`
	VAT   int64  `json:"vat"`
	Rate  string `json:"rate"`
	Known bool   `json:"known"`
}

// GrossTax uses exact integer arithmetic and rounds VAT to the nearest kopeck.
// An absent profile rate is not silently treated as no VAT.
func GrossTax(gross int64, profileRate string) (TaxTotal, error) {
	if gross < 0 {
		return TaxTotal{}, fmt.Errorf("negative report amount")
	}
	rate, percent := "", int64(0)
	switch profileRate {
	case "none":
		rate = "100"
	case "vat_0":
		rate = "0"
	case "vat_5":
		rate, percent = "5", 5
	case "vat_7":
		rate, percent = "7", 7
	case "vat_10":
		rate, percent = "10", 10
	case "vat_20":
		rate, percent = "20", 20
	case "vat_22":
		rate, percent = "22", 22
	default:
		return TaxTotal{}, fmt.Errorf("report VAT rate is missing or unsupported")
	}
	n := new(big.Int).Mul(big.NewInt(gross), big.NewInt(percent))
	d := big.NewInt(100 + percent)
	n.Add(n, new(big.Int).Div(new(big.Int).Set(d), big.NewInt(2)))
	n.Div(n, d)
	return TaxTotal{Gross: gross, VAT: n.Int64(), Rate: rate, Known: true}, nil
}

func (t *TaxTotal) Add(other TaxTotal) {
	if !t.Known && t.Gross == 0 && t.VAT == 0 {
		*t = other
		return
	}
	t.Gross += other.Gross
	t.VAT += other.VAT
	t.Known = t.Known && other.Known
	if t.Rate != other.Rate {
		t.Rate = ""
	}
}

type ReportFinance struct {
	Services   TaxTotal `json:"services"`
	Commission TaxTotal `json:"commission"`
}

func (f ReportFinance) Valid(gross int64) bool {
	return f.Services.Known && f.Services.Gross == gross && f.Services.VAT >= 0 && f.Services.VAT <= gross
}
