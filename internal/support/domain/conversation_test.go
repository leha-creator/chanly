package domain

import (
	"strings"
	"testing"
)

func TestReplyAndRead(t *testing.T) {
	for _, tt := range []struct {
		text string
		n    int
		ok   bool
	}{{"", 0, false}, {"", 1, true}, {"Привет", 0, true}, {strings.Repeat("я", 4000), 0, true}, {strings.Repeat("я", 4001), 0, false}, {"x", 6, false}} {
		if (ValidateReply(tt.text, tt.n) == nil) != tt.ok {
			t.Fatalf("reply length=%d attachments=%d", len(tt.text), tt.n)
		}
	}
	if AdvanceRead(4, 2, 10) != 4 || AdvanceRead(4, 20, 10) != 10 {
		t.Fatal("read marker regressed or exceeded history")
	}
	if !ReplyNeeded(11, 10) || ReplyNeeded(10, 10) {
		t.Fatal("new incoming must remain unanswered")
	}
}
