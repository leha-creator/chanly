package domain

import (
	"fmt"
	"net/url"
	"time"
)

func validDocumentURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(raw) <= 2000
}

func (d PayoutDocuments) Merge(next PayoutDocuments) (PayoutDocuments, error) {
	for _, pair := range []struct {
		value string
		dest  *string
	}{
		{next.AgreementURL, &d.AgreementURL}, {next.AgreementSigningURL, &d.AgreementSigningURL},
		{next.AcceptanceURL, &d.AcceptanceURL}, {next.AcceptanceSigningURL, &d.AcceptanceSigningURL},
	} {
		if !validDocumentURL(pair.value) {
			return PayoutDocuments{}, fmt.Errorf("%w: invalid document URL", ErrInvalidPayout)
		}
		if pair.value != "" {
			*pair.dest = pair.value
		}
	}
	if !validDocumentURL(next.ReceiptURL) || !validDocumentURL(next.ManualFormURL) {
		return d, fmt.Errorf("%w: invalid document URL", ErrInvalidPayout)
	}
	for _, list := range [][]ReceiptDocument{next.FNS, next.Fiscal} {
		for _, doc := range list {
			if !validDocumentURL(doc.URL) || !validDocumentURL(doc.RefundURL) {
				return d, fmt.Errorf("%w: invalid receipt URL", ErrInvalidPayout)
			}
		}
	}
	if next.ReceiptURL != "" {
		d.ReceiptURL = next.ReceiptURL
	}
	merge := func(old, in []ReceiptDocument) []ReceiptDocument {
		out := append([]ReceiptDocument(nil), old...)
		for _, doc := range in {
			if doc.URL == "" {
				continue
			}
			found := false
			for i, v := range out {
				if v.URL == doc.URL {
					out[i] = doc
					found = true
					break
				}
			}
			if !found {
				out = append(out, doc)
			}
		}
		return out
	}
	d.FNS = merge(d.FNS, next.FNS)
	d.Fiscal = merge(d.Fiscal, next.Fiscal)
	if len(d.FNS)+len(d.Fiscal) > 50 {
		return PayoutDocuments{}, fmt.Errorf("%w: too many receipt links", ErrInvalidPayout)
	}
	if next.ManualRequired != nil {
		d.ManualRequired = next.ManualRequired
		d.ManualStatus = next.ManualStatus
		d.ManualFormURL = next.ManualFormURL
	}
	return d, nil
}

func (d PayoutDocuments) Ready(employment string) bool {
	if d.ManualRequired != nil && *d.ManualRequired && d.ManualStatus != "applied" {
		return false
	}
	return employment != "selfemployed" || d.ReceiptURL != "" || len(d.FNS) > 0
}

func (p *Payout) AttachReceipt(receipt string, at time.Time) (bool, error) {
	if p.Status != StatusSucceeded {
		return false, ErrInvalidPayoutState
	}
	if receipt == "" || receipt == p.ReceiptURL {
		return false, nil
	}
	if !validDocumentURL(receipt) {
		return false, ErrInvalidPayout
	}
	p.ReceiptURL = receipt
	p.UpdatedAt = at
	return true, nil
}
