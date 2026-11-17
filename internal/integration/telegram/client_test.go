package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
)

type callerFunc func(context.Context, outbound.Call) (outbound.Response, error)

func (f callerFunc) Do(ctx context.Context, c outbound.Call) (outbound.Response, error) {
	return f(ctx, c)
}

func TestPrivateStartProof(t *testing.T) {
	token := strings.Repeat("x", 43)
	for _, tc := range []struct {
		name, chat, text string
		from, chatID     int64
		bot, want        bool
	}{
		{"private", "private", "/start notify_" + token, 41, 41, false, true},
		{"named", "private", "/start@LordChanlyBot notify_" + token, 41, 41, false, true},
		{"group", "group", "/start notify_" + token, 41, -41, false, false},
		{"bot", "private", "/start notify_" + token, 41, 41, true, false},
		{"foreign chat", "private", "/start notify_" + token, 41, 42, false, false},
		{"wrong bot", "private", "/start@OtherBot notify_" + token, 41, 41, false, false},
		{"no token", "private", "/start", 41, 41, false, false},
		{"invalid token", "private", "/start notify_" + strings.Repeat("!", 43), 41, 41, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"update_id":7,"message":{"text":%q,"from":{"id":%d,"is_bot":%t},"chat":{"id":%d,"type":%q}}}`, tc.text, tc.from, tc.bot, tc.chatID, tc.chat))
			id, start, ok, err := ParseStart(raw, "@LordChanlyBot", func(s string) string {
				if s != token {
					t.Fatal("wrong token")
				}
				return "hash"
			})
			if err != nil || ok != tc.want || id != 7 {
				t.Fatalf("id=%d ok=%v err=%v", id, ok, err)
			}
			if ok && (start.UserID != 41 || start.TokenHash != "hash") {
				t.Fatal(start)
			}
		})
	}
	if _, _, _, err := ParseStart([]byte(`{}`), "", nil); err == nil {
		t.Fatal("missing update id accepted")
	}
}

func TestDeliveryProtocolAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		permanent bool
		retry     time.Duration
	}{
		{"ok", 200, `{"ok":true,"result":{}}`, false, 0},
		{"blocked", 403, `{"ok":false,"error_code":403}`, true, 0},
		{"body rejected", 200, `{"ok":false,"error_code":400}`, true, 0},
		{"flood", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":87}}`, false, 87 * time.Second},
		{"malformed", 200, `bad`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{Token: "123:test", Outbound: callerFunc(func(_ context.Context, call outbound.Call) (outbound.Response, error) {
				if !call.Sensitive || !call.NoRetry || call.URL != "https://api.telegram.org/bot123:test/sendMessage" {
					t.Fatal("unsafe call")
				}
				b, _ := json.Marshal(call.Body)
				if strings.Contains(string(b), "parse_mode") || !strings.Contains(string(b), `"chat_id":41`) {
					t.Fatal("incorrect body")
				}
				r := outbound.Response{Status: tc.status, Body: []byte(tc.body)}
				if tc.status >= 400 {
					return r, job.Permanent(errors.New("provider error"))
				}
				return r, nil
			})}
			err := c.SendUserMessage(t.Context(), 41, "<b>plain text</b>")
			if tc.name == "ok" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || job.IsPermanent(err) != tc.permanent {
				t.Fatalf("err=%v", err)
			}
			if tc.retry > 0 {
				if delay, ok := job.RetryAfter(err); !ok || delay != tc.retry {
					t.Fatalf("retry=%v", delay)
				}
			}
		})
	}
}
