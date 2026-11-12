package mail

import (
	"context"
	"time"
)

func (s *Sender) SendEmailChangeCode(ctx context.Context, email, code, purpose string, ttl time.Duration) error {
	subject, text, html, err := s.RenderEmailChangeCode(code, purpose, ttl)
	if err != nil {
		return err
	}
	return s.send(ctx, "Mail.SendEmailChangeCode", "email_change", email, subject, text, html)
}

func (s *Sender) RenderEmailChangeCode(code, purpose string, ttl time.Duration) (string, string, string, error) {
	data := struct {
		Code, Purpose  string
		ExpiresMinutes int
	}{code, purpose, int((ttl + time.Minute - 1) / time.Minute)}
	return s.emailChange.render(data)
}
