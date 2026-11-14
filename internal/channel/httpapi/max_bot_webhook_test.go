package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/httpapi"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type captureMAXEvent struct{ events []inbound.Event }

func (c *captureMAXEvent) Record(_ context.Context, event inbound.Event) (bool, error) {
	c.events = append(c.events, event)
	return false, nil
}

func TestNotificationMAXTokenRedaction(t *testing.T) {
	token := strings.Repeat("A", 43)
	pepper := []byte("test-pepper")
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("notification-max-bot:" + token))
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, payload, payloadKey string
		wantHash                  bool
		duplicatePayload          bool
	}{
		{"verified notification token", "notify_" + token, "payload", true, false},
		{"uppercase payload", "notify_" + token, "PAYLOAD", true, false},
		{"mixed case payload", "notify_" + token, "Payload", true, false},
		{"ambiguous payload variants", "notify_" + token, "PAYLOAD", false, true},
		{"forged internal field", "", "payload", false, false},
		{"short notification token", "notify_short", "payload", false, false},
		{"invalid notification token", "notify_" + strings.Repeat("!", 43), "payload", false, false},
		{"channel connection token", "connect_" + token, "payload", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureMAXEvent{}
			handler := httpapi.NewWebhook(capture, nil, clock.NewManual(time.Now()), logx.Discard(), nil, httpapi.WebhookOptions{
				PathSecret: pathSecret, HeaderSecret: headerSecret, SecretHeader: header, ConnectionPepper: pepper,
			})
			body := map[string]any{"update_type": "bot_started", tc.payloadKey: tc.payload, "notification_token_hash": "forged", "connection_token_hash": "forged", "NOTIFICATION_TOKEN_HASH": "forged", "Connection_Token_Hash": "forged", "user": map[string]any{"user_id": 5001}}
			if tc.duplicatePayload {
				body["payload"] = "notify_" + token
			}
			data, _ := json.Marshal(body)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/max/"+pathSecret, strings.NewReader(string(data)))
			request.SetPathValue("secret", pathSecret)
			request.Header.Set(header, headerSecret)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || len(capture.events) != 1 {
				t.Fatalf("response=%d events=%d", response.Code, len(capture.events))
			}
			var stored map[string]json.RawMessage
			if err := json.Unmarshal(capture.events[0].Payload, &stored); err != nil {
				t.Fatal(err)
			}
			for key := range stored {
				if strings.EqualFold(key, "payload") {
					t.Fatal("raw token payload persisted")
				}
			}
			var hash string
			_ = json.Unmarshal(stored["notification_token_hash"], &hash)
			if tc.wantHash && hash != expected {
				t.Fatalf("hash mismatch: %q", hash)
			}
			if !tc.wantHash && hash != "" {
				t.Fatal("forged notification hash persisted")
			}
			if strings.Contains(string(capture.events[0].Payload), token) || strings.Contains(string(capture.events[0].Payload), "forged") {
				t.Fatal("secret or forged field persisted")
			}
		})
	}
}
