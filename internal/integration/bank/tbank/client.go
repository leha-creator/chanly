// Package tbank reads T-API statements and can issue invoice documents through
// a separately enabled client. It has no outgoing payment creation methods.
package tbank

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

const SandboxURL = "https://business.tbank.ru/openapi/sandbox"
const ProductionURL = "https://business.tbank.ru/openapi"

type Transport interface {
	Do(context.Context, outbound.Call) (outbound.Response, error)
}
type Limiter interface {
	Wait(context.Context, ratelimit.Bucket, ratelimit.Priority) error
}
type Options struct {
	BaseURL, Token, Account, INN string
	Rate                         float64
}
type Client struct {
	out     Transport
	limiter Limiter
	opts    Options
	log     *slog.Logger
}

func New(out Transport, limiter Limiter, opts Options, log *slog.Logger) *Client {
	if opts.Rate <= 0 {
		opts.Rate = 5
	}
	return &Client{out: out, limiter: limiter, opts: opts, log: log}
}
func (c *Client) ReadPage(ctx context.Context, q domain.BankQuery) (domain.BankPage, error) {
	if q.Account != c.opts.Account || !q.From.Before(q.To) || len(q.Cursor) > 100 {
		return domain.BankPage{}, errors.New("tbank: invalid query")
	}
	if err := c.limiter.Wait(ctx, ratelimit.Bucket{Key: "tbank:global", Rate: c.opts.Rate, Burst: c.opts.Rate}, ratelimit.PriorityPublish); err != nil {
		return domain.BankPage{}, err
	}
	v := url.Values{"accountNumber": {q.Account}, "from": {q.From.UTC().Format(time.RFC3339Nano)}, "to": {q.To.UTC().Format(time.RFC3339Nano)}, "operationStatus": {"Transaction"}, "limit": {"1000"}}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	} else {
		v.Set("withBalances", "true")
	}
	requestID := uuid.NewString()
	c.log.DebugContext(ctx, "bank statement page requested", "bank_request_id", requestID, "from", q.From, "to", q.To, "continuation", q.Cursor != "")
	resp, err := c.out.Do(ctx, outbound.Call{Sensitive: true, Operation: "TBank.Statement", Method: http.MethodGet, URL: strings.TrimRight(c.opts.BaseURL, "/") + "/api/v1/statement?" + v.Encode(), Header: http.Header{"Authorization": {"Bearer " + c.opts.Token}, "X-Request-Id": {requestID}, "Accept": {"application/json"}}})
	if err != nil {
		c.log.ErrorContext(ctx, "bank statement read failed", "bank_request_id", requestID, "http_status", resp.Status)
		return domain.BankPage{}, err
	}
	var result result
	if json.Unmarshal(resp.Body, &result) != nil || result.Operations == nil || len(result.NextCursor) > 100 {
		return domain.BankPage{}, errors.New("tbank: incomplete statement response")
	}
	page, err := normalize(result, q, c.opts.INN)
	if err == nil {
		c.log.InfoContext(ctx, "bank statement page read", "bank_request_id", requestID, "operations", page.Operations, "incoming", len(page.Incoming), "rejected", page.Rejected)
	}
	return page, err
}
