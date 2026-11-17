// Package telegram provides private notification delivery through Telegram Bot API.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

type Caller interface {
	Do(context.Context, outbound.Call) (outbound.Response, error)
}
type Limiter interface {
	Wait(context.Context, ratelimit.Bucket, ratelimit.Priority) error
}
type Client struct {
	Outbound Caller
	Limiter  Limiter
	Token    string
}

func (c *Client) call(ctx context.Context, method string, body any) error {
	return c.callResult(ctx, method, body, nil)
}

func (c *Client) callResult(ctx context.Context, method string, body, target any) error {
	r, err := c.Outbound.Do(ctx, outbound.Call{
		Operation: method, Method: http.MethodPost,
		URL:  "https://api.telegram.org/bot" + c.Token + "/" + method,
		Body: body, Sensitive: true, NoRetry: true, ExpectedRejections: []int{400, 403},
	})
	var result struct {
		OK         bool            `json:"ok"`
		Code       int             `json:"error_code"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if len(r.Body) > 0 && json.Unmarshal(r.Body, &result) == nil {
		if r.Status == 429 || result.Code == 429 {
			delay := time.Duration(result.Parameters.RetryAfter) * time.Second
			if delay < time.Second {
				delay = time.Second
			}
			return job.Retry(errors.New("telegram: rate limited"), delay)
		}
		if err != nil {
			return err
		}
		if result.OK {
			if target != nil && json.Unmarshal(result.Result, target) != nil {
				return errors.New("telegram: invalid result")
			}
			return nil
		}
		if result.Code >= 400 && result.Code < 500 {
			return job.Permanent(errors.New("telegram: request rejected"))
		}
		return errors.New("telegram: request failed")
	}
	if err != nil {
		return err
	}
	return errors.New("telegram: invalid response")
}

func (c *Client) SendUserMessage(ctx context.Context, userID int64, text string) error {
	if userID <= 0 {
		return job.Permanent(errors.New("telegram: invalid private recipient"))
	}
	if c.Limiter != nil {
		for _, bucket := range []ratelimit.Bucket{
			{Key: "telegram:global", Rate: 25, Burst: 25},
			{Key: "telegram:user:" + strconv.FormatInt(userID, 10), Rate: 1, Burst: 1},
		} {
			if err := c.Limiter.Wait(ctx, bucket, ratelimit.PriorityReply); err != nil {
				return err
			}
		}
	}
	return c.call(ctx, "sendMessage", map[string]any{"chat_id": userID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
}

func (c *Client) SetWebhook(ctx context.Context, url, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{"url": url, "secret_token": secret, "allowed_updates": []string{"message"}, "drop_pending_updates": false})
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]bool{"drop_pending_updates": false})
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]json.RawMessage, error) {
	var updates []json.RawMessage
	err := c.callResult(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 10, "limit": 100, "allowed_updates": []string{"message"}}, &updates)
	return updates, err
}

// Start contains no original text, username, or bearer token.
type Start struct {
	UserID    int64  `json:"user_id"`
	TokenHash string `json:"token_hash"`
}

// ParseStart accepts only a human's /start command in their own private chat.
func ParseStart(raw []byte, botName string, hash func(string) string) (int64, Start, bool, error) {
	var u struct {
		ID      *int64 `json:"update_id"`
		Message *struct {
			Text string `json:"text"`
			From struct {
				ID    int64 `json:"id"`
				IsBot bool  `json:"is_bot"`
			} `json:"from"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &u); err != nil || u.ID == nil || *u.ID < 0 {
		return 0, Start{}, false, errors.New("telegram: malformed update")
	}
	m := u.Message
	if m == nil || m.Chat.Type != "private" || m.From.ID <= 0 || m.From.IsBot || m.Chat.ID != m.From.ID {
		return *u.ID, Start{}, false, nil
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 || len(fields) > 2 || (fields[0] != "/start" && !strings.EqualFold(fields[0], "/start@"+strings.TrimPrefix(botName, "@"))) {
		return *u.ID, Start{}, false, nil
	}
	if len(fields) == 1 {
		return *u.ID, Start{UserID: m.From.ID}, true, nil
	}
	token := strings.TrimPrefix(fields[1], "notify_")
	if token == fields[1] || len(token) != 43 || strings.ContainsAny(token, "+/=") {
		return *u.ID, Start{}, false, nil
	}
	for _, ch := range token {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return *u.ID, Start{}, false, nil
		}
	}
	return *u.ID, Start{UserID: m.From.ID, TokenHash: hash(token)}, true, nil
}
