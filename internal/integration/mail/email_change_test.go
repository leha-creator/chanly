package mail_test

import (
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/mail"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestEmailChangePurposeTemplates(t *testing.T) {
	s, err := mail.New(mail.Options{}, clock.NewManual(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{"current", "new"} {
		subject, text, html, err := s.RenderEmailChangeCode("123456", purpose, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(subject, "смены почты") || strings.Contains(subject, "123456") || !strings.Contains(text, "123456") || !strings.Contains(html, "123456") {
			t.Fatal("wrong confirmation template")
		}
		if purpose == "current" && !strings.Contains(text, "владение аккаунтом") {
			t.Fatal("current email purpose missing")
		}
		if purpose == "new" && !strings.Contains(text, "новый адрес") {
			t.Fatal("new email purpose missing")
		}
	}
}
