package domain

import (
	"context"
	"crypto/hmac"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

type SettingsAccess struct {
	AdminRole                   *string
	Advertiser, Blogger, MaxBot bool
}
type EmailChange struct {
	ID, UserID                                   uuid.UUID
	CurrentEmail, NewEmail, CurrentHash, NewHash string
	Attempts, Sends                              int
	SentAt, ExpiresAt, CreatedAt                 time.Time
	ConsumedAt                                   *time.Time
}

// Hashes bind codes to the user, operation, address and challenge; login codes cannot be reused.
func (c EmailChange) Hash(pepper []byte, purpose, code string) string {
	return HashCode(pepper, "email-change:"+c.UserID.String()+":"+c.ID.String()+":"+purpose+":"+c.NewEmail, code)
}
func (c *EmailChange) Verify(pepper []byte, currentCode, newCode string, now time.Time) error {
	if c.ConsumedAt != nil {
		return apperr.Validation("code", "consumed_code")
	}
	if !now.Before(c.ExpiresAt) {
		return apperr.Validation("code", "expired_code")
	}
	if c.Attempts >= 5 {
		return apperr.RateLimited("email_change_attempts", max(c.ExpiresAt.Sub(now), time.Second))
	}
	c.Attempts++
	currentOK := hmac.Equal([]byte(c.CurrentHash), []byte(c.Hash(pepper, "current", currentCode)))
	newOK := hmac.Equal([]byte(c.NewHash), []byte(c.Hash(pepper, "new", newCode)))
	if !currentOK || !newOK {
		return apperr.Validation("code", "invalid_code")
	}
	c.ConsumedAt = &now
	return nil
}

type AccountSettingsRepository interface {
	LockEmails(context.Context, []string) error
	Access(context.Context, uuid.UUID) (SettingsAccess, error)
	ListSessions(context.Context, uuid.UUID, time.Time) ([]Session, error)
	RevokeOtherSession(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) (bool, error)
	RevokeOtherSessions(context.Context, uuid.UUID, uuid.UUID, time.Time) error
	CountEmailChanges(context.Context, uuid.UUID, time.Time) (int, error)
	ConsumeEmailChanges(context.Context, uuid.UUID, time.Time) error
	InsertEmailChange(context.Context, *EmailChange) error
	LockEmailChange(context.Context, uuid.UUID, uuid.UUID) (*EmailChange, error)
	SaveEmailChange(context.Context, *EmailChange) error
	ChangeEmail(context.Context, uuid.UUID, string, time.Time) error
}

type EmailChangeCodeSender interface {
	SendEmailChangeCode(context.Context, string, string, string, time.Duration) error
}
