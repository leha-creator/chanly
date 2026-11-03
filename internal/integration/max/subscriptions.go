package max

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// SecretHeader — заголовок, в котором MAX передаёт секрет подписки (Q-35, R-3).
const SecretHeader = "X-Max-Bot-Api-Secret"

// Subscription — подписка бота на Webhook (GET /subscriptions).
type Subscription struct {
	URL         string   `json:"url"`
	UpdateTypes []string `json:"update_types"`
}

type newSubscription struct {
	URL         string   `json:"url"`
	UpdateTypes []string `json:"update_types,omitempty"`
	Secret      string   `json:"secret,omitempty"`
}

// ListSubscriptions — текущие подписки бота.
func (c *Client) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	resp, err := c.do(ctx, request{
		operation: "ListSubscriptions", method: http.MethodGet, path: "/subscriptions",
		redact: []string{"url"},
	})
	if err != nil {
		return nil, err
	}
	var b struct {
		Subscriptions []Subscription `json:"subscriptions"`
	}
	if err := json.Unmarshal(resp.Body, &b); err != nil {
		return nil, job.Permanent(fmt.Errorf("max ListSubscriptions: decode response: %w", err))
	}
	return b.Subscriptions, nil
}

// Subscribe подписывает бота на Webhook (R-5). Адрес содержит секретную часть пути,
// поэтому в outbound_request и журнал он не попадает.
func (c *Client) Subscribe(ctx context.Context, webhookURL string, updateTypes []string, secret string) error {
	if _, err := c.do(ctx, request{
		operation: "Subscribe", method: http.MethodPost, path: "/subscriptions",
		body:   newSubscription{URL: webhookURL, UpdateTypes: updateTypes, Secret: secret},
		redact: []string{"url"},
	}); err != nil {
		return err
	}
	c.log.InfoContext(ctx, "max webhook subscribed", logx.KeyOp, "MAX.Subscribe", "host", hostOf(webhookURL), "update_types", updateTypes)
	return nil
}

// hostOf — адрес без пути: путь Webhook содержит секрет.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
