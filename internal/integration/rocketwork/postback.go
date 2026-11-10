package rocketwork

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Public API v1 postback subscriptions with authorization_credentials.type=Bearer.
// This token authenticates transport; it is not a cryptographic body signature.
func parseTaskPostback(secret []byte, header map[string][]string, body []byte) (Event, error) {
	return parsePostbackSecrets([][]byte{secret}, header, body)
}

func parsePostbackSecrets(secrets [][]byte, header map[string][]string, body []byte) (Event, error) {
	if len(body) > MaxWebhookBody {
		return Event{}, fmt.Errorf("%w: body too large", ErrFormat)
	}
	valid := 0
	for _, secret := range secrets {
		if len(secret) != 0 {
			valid |= subtle.ConstantTimeCompare([]byte(headerValue(header, "Authorization")), []byte("Bearer "+string(secret)))
		}
	}
	if valid != 1 {
		return Event{}, ErrSignature
	}
	return decodePostback(body)
}

// decodePostback is also used for authenticated history responses. It never
// retains provider data or executor PII in the inbound journal.
func decodePostback(body []byte) (Event, error) {
	var d struct {
		ID         int64  `json:"id"`
		Event      string `json:"event"`
		HappenedAt string `json:"happened_at"`
		Data       struct {
			INN   string `json:"inn"`
			Phone string `json:"phone"`
			ID    int64  `json:"id"`
			Task  *struct {
				ID int64 `json:"id"`
			} `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return Event{}, ErrFormat
	}
	if _, err := time.Parse(time.RFC3339Nano, d.HappenedAt); err != nil {
		return Event{}, ErrFormat
	}
	if d.ID <= 0 {
		return Event{}, ErrFormat
	}
	event := Event{EventID: strconv.FormatInt(d.ID, 10)}
	switch d.Event {
	case "task.inwork", "task.accepting", "task.completed", "task.paid", "task.error", "task.canceled":
	case "executors.activated":
		if d.Data.INN == "" && d.Data.Phone == "" {
			return Event{}, ErrFormat
		}
		event.Kind = "executor_refresh"
		return event, nil
	case "company_accounts.balance_changed", "company_accounts.income":
		event.Kind = "deposit_refresh"
		return event, nil
	default:
		if d.Event == "" {
			return Event{}, ErrFormat
		}
		event.Kind = "unknown"
		return event, nil
	}
	id := d.Data.ID
	// The documentation shows flat task serialization and references the GET
	// task wrapper. Accept either, but reject conflicting identities.
	if d.Data.Task != nil {
		if id != 0 && id != d.Data.Task.ID {
			return Event{}, ErrFormat
		}
		id = d.Data.Task.ID
	}
	if id <= 0 || d.ID <= 0 {
		return Event{}, ErrFormat
	}
	return Event{Kind: EventPaymentStatus, ObjectID: strconv.FormatInt(id, 10), EventID: strconv.FormatInt(d.ID, 10)}, nil
}
