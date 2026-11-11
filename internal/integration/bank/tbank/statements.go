package tbank

import (
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// decimal converts JSON decimal exactly, including exponent notation, without rounding.
func decimal(n json.Number) (*money.Kopecks, error) {
	if n == "" {
		return nil, nil
	}
	if len(n) > 100 {
		return nil, errors.New("tbank: invalid amount")
	}
	if i := strings.IndexAny(string(n), "eE"); i >= 0 {
		exp, err := strconv.Atoi(string(n)[i+1:])
		if err != nil || exp < -100 || exp > 100 {
			return nil, errors.New("tbank: decimal exponent out of range")
		}
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return nil, errors.New("tbank: invalid amount")
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return nil, errors.New("tbank: non-kopeck or overflowing amount")
	}
	k := money.Kopecks(r.Num().Int64())
	return &k, nil
}

func normalize(r result, q domain.BankQuery, ownINN string) (domain.BankPage, error) {
	p := domain.BankPage{NextCursor: r.NextCursor, Operations: len(r.Operations)}
	if r.Balances != nil && q.Cursor == "" {
		var err error
		for _, pair := range []struct {
			n    json.Number
			dest **money.Kopecks
		}{{r.Balances.Opening, &p.Opening}, {r.Balances.Closing, &p.Closing}, {r.Balances.Credit, &p.Credit}, {r.Balances.Debit, &p.Debit}} {
			if *pair.dest, err = decimal(pair.n); err != nil {
				return p, err
			}
		}
	}
	for _, o := range r.Operations {
		if o.Payer != nil {
			o.Payer.Account = strings.TrimSpace(o.Payer.Account)
			o.Payer.INN = strings.TrimSpace(o.Payer.INN)
		}
		if o.Status != "Transaction" || o.Type != "Credit" {
			p.Rejected++
			continue
		}
		amount, err := decimal(o.Amount)
		if err != nil {
			return p, err
		}
		if o.ID == "" || o.Date.IsZero() || o.Date.Before(q.From) || !o.Date.Before(q.To) || o.Account != q.Account || o.Currency != "643" || amount == nil || *amount <= 0 || o.Payer == nil || o.Payer.Account == "" || o.Payer.INN == "" || o.Payer.Account == q.Account || (ownINN != "" && o.Payer.INN == ownINN) || (o.Receiver != nil && o.Receiver.Account != "" && o.Receiver.Account != q.Account) {
			p.Rejected++
			continue
		}
		in := domain.Incoming{ExternalID: "tbank:" + q.Account + ":" + o.ID, OperationDate: o.Date.UTC(), Amount: *amount, PayerINN: o.Payer.INN, PayerName: o.Payer.Name, PayerAccount: o.Payer.Account, Purpose: o.Purpose, BankOperationID: o.ID, DocumentNumber: strings.TrimSpace(o.Number), DocDate: o.DocDate, ChargeDate: o.ChargeDate}
		// Dates remain UTC. Cross-source date equivalence is deliberately conservative.
		if o.DocDate == nil || o.ChargeDate == nil || in.DocumentNumber == "" {
			in.IdentityConflict = true
		}
		if o.DocDate != nil {
			d := o.DocDate.UTC()
			in.DocDate = &d
		}
		if o.ChargeDate != nil {
			d := o.ChargeDate.UTC()
			in.ChargeDate = &d
		}
		// Until the bank confirms the 1C time-zone mapping, only dates unambiguous
		// in UTC and Moscow are eligible for automatic cross-source identification.
		for _, d := range []*time.Time{in.DocDate, in.ChargeDate} {
			if d != nil && d.UTC().Format(time.DateOnly) != d.In(time.FixedZone("MSK", 3*3600)).Format(time.DateOnly) {
				in.IdentityConflict = true
			}
		}
		p.Incoming = append(p.Incoming, in)
	}
	return p, nil
}
