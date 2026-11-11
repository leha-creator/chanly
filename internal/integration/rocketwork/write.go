package rocketwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// ErrUncertain means a write may have reached the provider. Only authoritative
// reconciliation can resolve it; a retry must not repeat the write.
var ErrUncertain = errors.New("rocketwork: write outcome uncertain")

func (c *Client) write(ctx context.Context, op, method, path string, body, result any) error {
	return c.writeWithTask(ctx, op, method, path, body, result, nil)
}

func (c *Client) writeWithTask(ctx context.Context, op, method, path string, body, result any, before *TaskSnapshot) error {
	data, err := json.Marshal(body)
	if err != nil {
		return ErrFormat
	}
	if c.opts.Authorizer == nil {
		c.log.WarnContext(ctx, "rocketwork policy_denied", "operation", op, "reason", "readonly")
		return ErrPolicyDenied
	}
	c.log.DebugContext(ctx, "rocketwork write started", "operation", op)
	if err := c.limiter.Wait(ctx, ratelimit.Bucket{Key: "rocketwork:global", Rate: c.opts.Rate, Burst: c.opts.Rate}, ratelimit.PriorityPublish); err != nil {
		return fmt.Errorf("%w: write not sent", ErrUnavailable)
	}
	// Intent is committed immediately before the first possible network effect.
	if err := c.opts.Authorizer.AuthorizeWrite(ctx, WriteRequest{Operation: op, Method: method, Path: path, Body: data, Before: before}); err != nil {
		c.log.WarnContext(ctx, "rocketwork policy_denied", "operation", op, "reason", "authorization")
		return ErrPolicyDenied
	}
	response, err := c.out.Do(ctx, outbound.Call{Operation: "RocketWork." + op, Method: method, URL: c.opts.BaseURL + path, Body: body, NoRetry: true, Sensitive: true, Redact: RedactFields, Header: http.Header{"Authorization": {"Bearer " + c.opts.Token}, "Accept": {"application/json"}, "Content-Type": {"application/json"}, "User-Agent": {"maxim-rocketwork/1"}}})
	if err != nil {
		c.log.WarnContext(ctx, "rocketwork write not confirmed", "operation", op, "status", response.Status)
		switch response.Status {
		case 400, 401, 403, 404, 422:
			return &RejectedError{Code: FailureProviderError, Message: fmt.Sprintf("HTTP %d", response.Status)}
		}
		return ErrUncertain
	}
	if err := json.Unmarshal(response.Body, result); err != nil {
		return ErrUncertain
	}
	c.log.InfoContext(ctx, "rocketwork write response received", "operation", op, "status", response.Status)
	return nil
}
