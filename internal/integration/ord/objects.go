package ord

import (
	"context"
	"net/http"
	"net/url"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

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
func (c *Client) UpsertStatistics(ctx context.Context, s Statistics) (Receipt, error) {
	return c.submit(ctx, OpStatistics, s.ID, s)
}

// UpsertInvoice регистрирует или обновляет акт (R-22).
// ПРЕДПОЛОЖЕНИЕ (Q-42): путь /invoice.
func (c *Client) UpsertInvoice(ctx context.Context, inv Invoice) (Receipt, error) {
	return c.submit(ctx, OpInvoice, inv.ID, inv)
}

// UpsertInvoiceCreatives подаёт разбивку акта по креативам и площадкам (R-22).
// ПРЕДПОЛОЖЕНИЕ (Q-42): путь /invoice/creatives; идентификатор — id акта.
func (c *Client) UpsertInvoiceCreatives(ctx context.Context, ic InvoiceCreatives) (Receipt, error) {
	return c.submit(ctx, OpInvoiceCreatives, ic.InvoiceID, ic)
}
