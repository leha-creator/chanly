package rocketwork

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxWebhookBody — предел тела вебхука (R-11).
const MaxWebhookBody = 1 << 20

// SignatureHeader — заголовок подписи вебхука.
//
// Только локальный fake: HMAC-SHA256 в X-Rocketwork-Signature.
// Public API v1 использует Authorization подписки; Client этот формат не принимает.
const SignatureHeader = "X-Rocketwork-Signature"

const signaturePrefix = "sha256="

// Sign — подпись тела секретом (для подделки и тестов).
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature сравнивает подпись за постоянное время (07 §4). Пустой секрет — отказ.
func VerifySignature(secret []byte, header string, body []byte) error {
	if len(secret) == 0 || header == "" {
		return ErrSignature
	}
	if !hmac.Equal([]byte(strings.TrimSpace(header)), []byte(Sign(secret, body))) {
		return ErrSignature
	}
	return nil
}

// webhookBody — тело события.
//
// Протокол fake: {id, event, object_id}; событие несёт только
// идентификатор объекта, состояние запрашивается GetContractor / GetPayout.
type webhookBody struct {
	ID       string `json:"id"`
	Event    string `json:"event"`
	ObjectID string `json:"object_id"`
}

// ParseEvent разбирает тело уже проверенного вебхука.
func ParseEvent(body []byte) (Event, error) {
	var b webhookBody
	if err := json.Unmarshal(body, &b); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	kind, ok := EventKindFrom(b.Event)
	if !ok || b.ObjectID == "" {
		return Event{}, fmt.Errorf("%w: event %q object %q", ErrFormat, b.Event, b.ObjectID)
	}
	return Event{Kind: kind, ObjectID: b.ObjectID, EventID: b.ID}, nil
}

// ParseWebhook проверяет подпись и разбирает тело.
func ParseWebhook(secret []byte, header map[string][]string, body []byte) (Event, error) {
	if len(body) > MaxWebhookBody {
		return Event{}, fmt.Errorf("%w: body too large", ErrFormat)
	}
	if err := VerifySignature(secret, headerValue(header, SignatureHeader), body); err != nil {
		return Event{}, err
	}
	return ParseEvent(body)
}

func headerValue(h map[string][]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// WebhookPayload — тело события для подделки и тестов.
func WebhookPayload(eventID, serviceEvent, objectID string) []byte {
	b, _ := json.Marshal(map[string]string{"id": eventID, "event": serviceEvent, "object_id": objectID})
	return b
}

// Имена событий сервиса (для подделки).
//
// Только локальные сценарии; см. EventKindFrom.
const (
	ServiceEventContractor = "contractor.status_changed"
	ServiceEventPayment    = "payment.status_changed"
)
