package rocketwork

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type SubscriptionAuthorization struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Subscription contains a secret. Do not log or expose this wire value.
type Subscription struct {
	ID            int64                     `json:"id"`
	URI           string                    `json:"uri"`
	Method        string                    `json:"http_method"`
	Authorization SubscriptionAuthorization `json:"authorization_credentials"`
	RetryLimit    int                       `json:"retry_limit"`
	Version       string                    `json:"version"`
	Streams       []string                  `json:"subscribed_on"`
}

type SubscriptionRequest struct {
	URI           string                    `json:"uri"`
	Method        string                    `json:"http_method"`
	Authorization SubscriptionAuthorization `json:"authorization_credentials"`
	RetryLimit    int                       `json:"retry_limit"`
	Streams       []string                  `json:"subscribed_on"`
}

func (r SubscriptionRequest) valid() bool {
	u, err := url.Parse(r.URI)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || r.Method != "post" || r.Authorization.Type != "Bearer" || r.Authorization.Value == "" || r.RetryLimit < 1 || r.RetryLimit > 15 || len(r.Streams) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, stream := range r.Streams {
		if seen[stream] {
			return false
		}
		seen[stream] = true
		switch stream {
		case "tasks", "executors", "company_accounts":
		default:
			return false
		}
	}
	return true
}

func (s Subscription) Matches(r SubscriptionRequest) bool {
	if s.ID <= 0 || s.Version != "v2" || s.URI != r.URI || s.Method != r.Method || s.RetryLimit != r.RetryLimit || s.Authorization.Type != r.Authorization.Type || subtle.ConstantTimeCompare([]byte(s.Authorization.Value), []byte(r.Authorization.Value)) != 1 || len(s.Streams) != len(r.Streams) {
		return false
	}
	set := map[string]bool{}
	for _, v := range s.Streams {
		if set[v] {
			return false
		}
		set[v] = true
	}
	for _, v := range r.Streams {
		if !set[v] {
			return false
		}
	}
	return true
}

func (c *Client) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	var rows []Subscription
	if err := c.read(ctx, "ListSubscriptions", "/api/postback_subscriptions", nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, ErrFormat
	}
	seen := map[int64]bool{}
	for _, row := range rows {
		if row.ID <= 0 || seen[row.ID] {
			return nil, ErrFormat
		}
		seen[row.ID] = true
	}
	return rows, nil
}

// CreateSubscription must be preceded by a durable command checkpoint. After
// an uncertain response the caller must only list/reconcile, never POST again.
func (c *Client) CreateSubscription(ctx context.Context, r SubscriptionRequest) (Subscription, error) {
	if !r.valid() {
		return Subscription{}, ErrFormat
	}
	var result Subscription
	if err := c.write(ctx, "CreateSubscription", http.MethodPost, "/api/postback_subscriptions", r, &result); err != nil {
		return Subscription{}, err
	}
	if !result.Matches(r) {
		return Subscription{}, ErrUncertain
	}
	return result, nil
}

// DeleteSubscription first verifies the complete owned subscription, including
// secret and version. An arbitrary subscription ID alone is insufficient.
func (c *Client) DeleteSubscription(ctx context.Context, id int64, owned SubscriptionRequest) error {
	if id <= 0 || !owned.valid() {
		return ErrFormat
	}
	rows, err := c.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		if !row.Matches(owned) {
			return ErrFormat
		}
		var removed Subscription
		if err := c.write(ctx, "DeleteSubscription", http.MethodDelete, "/api/postback_subscriptions/"+strconv.FormatInt(id, 10), nil, &removed); err != nil {
			return err
		}
		if removed.ID != id || !removed.Matches(owned) {
			return ErrUncertain
		}
		return nil
	}
	return nil // authoritative list confirms absence
}

type HistoryPage struct {
	Events               []Event
	Current, Last, Total int
}

// PostbackHistory reads a bounded, closed interval. The caller commits its
// cursor only after every page has been durably ingested with event-ID dedupe.
func (c *Client) PostbackHistory(ctx context.Context, from, through time.Time, page int) (HistoryPage, error) {
	if from.IsZero() || through.Before(from) || page < 1 {
		return HistoryPage{}, ErrFormat
	}
	q := url.Values{"filter[happened_at_gte]": {from.UTC().Format(time.RFC3339Nano)}, "filter[happened_at_lte]": {through.UTC().Format(time.RFC3339Nano)}, "page[page]": {strconv.Itoa(page)}, "page[per_page]": {"100"}}
	var response struct {
		Postbacks []json.RawMessage `json:"postbacks"`
		Meta      *pageDTO          `json:"meta"`
	}
	if err := c.read(ctx, "PostbackHistory", "/api/postbacks", q, &response); err != nil {
		return HistoryPage{}, err
	}
	if response.Postbacks == nil || response.Meta.validate(page, len(response.Postbacks)) != nil {
		return HistoryPage{}, ErrFormat
	}
	result := HistoryPage{Current: page, Last: response.Meta.Last, Total: response.Meta.Total}
	for _, raw := range response.Postbacks {
		var meta struct {
			Status     string    `json:"status"`
			HappenedAt time.Time `json:"happened_at"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Status == "" || meta.HappenedAt.Before(from) || meta.HappenedAt.After(through) {
			return HistoryPage{}, ErrFormat
		}
		event, err := decodePostback(raw)
		if err != nil {
			return HistoryPage{}, err
		}
		result.Events = append(result.Events, event)
	}
	return result, nil
}
