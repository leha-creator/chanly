package ord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// SubmitSandbox submits a prepared API v8 fixture through the normal outbound
// journal and limiter. It is deliberately unavailable on the production endpoint.
// Callers must persist an attempt before calling and disable inline retries.
func (c *Client) SubmitSandbox(ctx context.Context, operation, objectID string, payload json.RawMessage) (Receipt, error) {
	if c.opts.BaseURL != TestBaseURL {
		return Receipt{}, errors.New("sandbox submission requires the ORD test endpoint")
	}
	switch operation {
	case OpOrganization, OpContract, OpPlatforms, OpCreative, OpStatistics, OpInvoice, OpInvoiceCreatives:
	default:
		return Receipt{}, errors.New("unsupported sandbox operation")
	}
	if objectID == "" || !json.Valid(payload) {
		return Receipt{}, errors.New("invalid sandbox fixture")
	}
	// Use the same typed wire bodies as the explicit v8 reporting client.
	// Reject unknown fields instead of silently discarding edited manifest data.
	switch operation {
	case OpStatistics:
		var body StatisticsV8
		if err := decodeSandboxReport(payload, &body); err != nil {
			return Receipt{}, err
		}
		return c.SubmitStatisticsV8(ctx, objectID, body)
	case OpInvoice:
		var body InvoiceV8
		if err := decodeSandboxReport(payload, &body); err != nil {
			return Receipt{}, err
		}
		if body.ID != objectID {
			return Receipt{}, errors.New("sandbox invoice identity mismatch")
		}
		return c.SubmitInvoiceV8(ctx, body)
	case OpInvoiceCreatives:
		var body InvoiceCreativesV8
		if err := decodeSandboxReport(payload, &body); err != nil {
			return Receipt{}, err
		}
		return c.SubmitInvoiceCreativesV8(ctx, objectID, body)
	}
	return c.submit(ctx, operation, objectID, payload)
}

func decodeSandboxReport(payload json.RawMessage, body any) error {
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if err := d.Decode(body); err != nil {
		return errors.New("invalid sandbox report fields")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid sandbox report JSON")
	}
	return nil
}

// GetOrganizationStatus reads the status/identity subset of ResponseGetObject.
// Personal organization data from the response is neither returned nor logged.
func (c *Client) GetOrganizationStatus(ctx context.Context, id string) (Receipt, error) {
	c.log.DebugContext(ctx, "ord organization lookup started", logx.KeyOp, "ORD.GetOrganizationStatus")
	rec, err := c.do(ctx, request{operation: OpOrganization, method: http.MethodGet, path: OpOrganization,
		query: url.Values{"object_id": {id}}, objectID: id})
	if err != nil {
		return Receipt{}, err
	}
	if rec.ObjectID != id {
		return Receipt{}, errors.New("ord organization response identity mismatch")
	}
	c.log.DebugContext(ctx, "ord organization lookup completed", logx.KeyOp, "ORD.GetOrganizationStatus")
	return rec, nil
}

// UpsertOrganization регистрирует или обновляет контрагента (05 §3.3).
func (c *Client) UpsertOrganization(ctx context.Context, o Organization) (Receipt, error) {
	return c.submit(ctx, OpOrganization, o.ID, o)
}

// UpsertContract регистрирует или обновляет договор (05 §3.3).
func (c *Client) UpsertContract(ctx context.Context, ct Contract) (Receipt, error) {
	return c.submit(ctx, OpContract, ct.ID, ct)
}

// UpsertPlatforms регистрирует или обновляет площадки организации (05 §3.3).
func (c *Client) UpsertPlatforms(ctx context.Context, p Platforms) (Receipt, error) {
	id := p.OrganizationID
	if len(p.Platforms) == 1 {
		id = p.Platforms[0].PlatformID
	}
	return c.submit(ctx, OpPlatforms, id, p)
}

// UpsertCreative регистрирует креатив; токен (erid) приходит в token ответа или в
// object_statuses[].token, иначе — при опросе статуса (05 §3.4).
func (c *Client) UpsertCreative(ctx context.Context, cr Creative) (Receipt, error) {
	return c.submit(ctx, OpCreative, cr.ID, cr)
}

// GetStatus читает статус запроса по reqid (05 §3.5).
func (c *Client) GetStatus(ctx context.Context, requestID string) (Receipt, error) {
	rec, err := c.do(ctx, request{operation: OpStatus, method: http.MethodGet, path: OpStatus,
		query: url.Values{"reqid": {requestID}}, objectID: requestID})
	if err != nil {
		return Receipt{}, err
	}
	c.log.DebugContext(ctx, "ord status read", logx.KeyOp, "ORD.GetStatus", "reqid", requestID,
		"ord_status", rec.Status, "errors", len(rec.ErrorMessage))
	return rec, nil
}

// UpsertStatistics подаёт статистику месяца по паре «креатив + площадка» (R-20).
// ПРЕДПОЛОЖЕНИЕ (Q-42): путь /statistics.
// Deprecated: legacy body is not API v8 compatible; use SubmitStatisticsV8 with explicit inputs.
func (c *Client) UpsertStatistics(ctx context.Context, s Statistics) (Receipt, error) {
	return c.submit(ctx, OpStatistics, s.ID, s)
}

// UpsertInvoice регистрирует или обновляет акт (R-22).
// ПРЕДПОЛОЖЕНИЕ (Q-42): путь /invoice.
// Deprecated: use SubmitInvoiceV8 and persist its independent receipt.
func (c *Client) UpsertInvoice(ctx context.Context, inv Invoice) (Receipt, error) {
	return c.submit(ctx, OpInvoice, inv.ID, inv)
}

// UpsertInvoiceCreatives подаёт разбивку акта по креативам и площадкам (R-22).
// ПРЕДПОЛОЖЕНИЕ (Q-42): путь /invoice/creatives; идентификатор — id акта.
// Deprecated: use SubmitInvoiceCreativesV8 with links grouped by initial contract.
func (c *Client) UpsertInvoiceCreatives(ctx context.Context, ic InvoiceCreatives) (Receipt, error) {
	return c.submit(ctx, OpInvoiceCreatives, ic.InvoiceID, ic)
}
