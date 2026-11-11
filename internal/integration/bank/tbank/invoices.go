package tbank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// InvoiceClient is deliberately separate from the statement reader. Enabling it
// requires an explicit billing provider setting; it never initiates transfers.
type InvoiceClient struct {
	Out                     Transport
	Limiter                 Limiter
	HTTP                    *http.Client
	BaseURL, Token, Account string
	PDFHosts                []string
}

var invoiceDigits = regexp.MustCompile(`^[1-9]\d{0,14}$`)

func (c *InvoiceClient) Send(ctx context.Context, doc domain.InvoiceDoc) (domain.BankInvoiceDocument, error) {
	// Production rejects zero-padded numbers even though its published schema
	// accepts them. Keep MX-padding only in our number and payment purpose.
	number := strings.TrimLeft(strings.TrimPrefix(doc.Number, "MX-"), "0")
	if !invoiceDigits.MatchString(number) || doc.Amount <= 0 || doc.Supplier.BankAccount != c.Account {
		return domain.BankInvoiceDocument{}, errors.New("tbank: invalid invoice")
	}
	if err := c.Limiter.Wait(ctx, ratelimit.Bucket{Key: "tbank:invoice", Rate: 4, Burst: 4}, ratelimit.PriorityPublish); err != nil {
		return domain.BankInvoiceDocument{}, err
	}
	// Use exact decimal rubles rather than a binary floating point conversion.
	price := json.Number(fmt.Sprintf("%d.%02d", doc.Amount/100, doc.Amount%100))
	payer := map[string]string{}
	if doc.Payer.INN != "" {
		payer["inn"] = doc.Payer.INN
	}
	if doc.Payer.Name != "" {
		payer["name"] = doc.Payer.Name
	}
	body := map[string]any{
		"invoiceNumber": number, "invoiceDate": doc.IssuedAt.In(time.FixedZone("MSK", 3*3600)).Format(time.DateOnly),
		"dueDate":       doc.ExpiresAt.In(time.FixedZone("MSK", 3*3600)).Format(time.DateOnly),
		"accountNumber": c.Account, "customPaymentPurpose": doc.PaymentPurpose,
		"payer": payer,
		"items": []map[string]any{{"name": "Пополнение баланса по счёту № " + doc.Number, "vat": "None", "price": price, "amount": 1, "unit": "шт."}},
	}
	// No contacts/phone: creating a document must not message the customer.
	resp, err := c.Out.Do(ctx, outbound.Call{Sensitive: true, NoRetry: true, Create: true,
		Operation: "TBank.InvoiceSend", Method: http.MethodPost, URL: strings.TrimRight(c.BaseURL, "/") + "/api/v1/invoice/send",
		Header: http.Header{"Authorization": {"Bearer " + c.Token}, "X-Request-Id": {uuid.NewString()}, "Accept": {"application/json"}}, Body: body})
	if err != nil {
		return domain.BankInvoiceDocument{}, errors.New("tbank: invoice outcome requires verification")
	}
	var result struct {
		ID     uuid.UUID `json:"invoiceId"`
		PDFURL string    `json:"pdfUrl"`
	}
	if resp.Status != http.StatusOK || json.Unmarshal(resp.Body, &result) != nil || result.ID == uuid.Nil || result.PDFURL == "" {
		return domain.BankInvoiceDocument{}, errors.New("tbank: incomplete invoice response")
	}
	return domain.BankInvoiceDocument{ID: result.ID, PDFURL: result.PDFURL}, nil
}

func (c *InvoiceClient) Download(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Fragment != "" {
		return nil, errors.New("tbank: invalid PDF URL")
	}
	allowed := false
	for _, host := range c.PDFHosts {
		if strings.EqualFold(u.Hostname(), strings.TrimSpace(host)) {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("tbank: PDF host is not allowed")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("tbank: invalid PDF request")
	}
	// The URL may be signed. Never send the API token or log its query string.
	req.Header.Set("Accept", "application/pdf")
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("tbank: PDF download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("tbank: PDF download failed")
	}
	const maxPDF = 10 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPDF+1))
	if err != nil || len(data) > maxPDF || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, errors.New("tbank: invalid PDF response")
	}
	return data, nil
}
