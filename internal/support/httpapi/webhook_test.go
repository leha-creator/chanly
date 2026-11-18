package httpapi

import "testing"

func TestIncomingMediaAndIsolation(t *testing.T) {
	in, ok := parseIncoming([]byte(`{"update_type":"message_created","timestamp":1234,"message":{"sender":{"user_id":9007199254740999,"name":"User"},"recipient":{"chat_type":"dialog"},"body":{"mid":"m1","attachments":[{"type":"audio","payload":{"url":"https://cdn.test/voice"}}]}}}`), "1")
	if !ok || in.UserID != "9007199254740999" || len(in.Media) != 1 || in.Media[0].Type != "audio" {
		t.Fatal("media-only or int64 lost")
	}
	for _, b := range []string{`{"update_type":"bot_started"}`, `{"update_type":"message_created","message":{"sender":{"user_id":2,"is_bot":true},"recipient":{"chat_type":"dialog"},"body":{"mid":"m1","text":"echo"}}}`, `{"update_type":"message_created","message":{"sender":{"user_id":2},"recipient":{"chat_type":"channel"},"body":{"mid":"m1","text":"post"}}}`} {
		if _, ok := parseIncoming([]byte(b), "1"); ok {
			t.Fatal("non-dialog accepted")
		}
	}
}
