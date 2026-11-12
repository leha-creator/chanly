package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func (s *Service) ListOwnSessions(ctx context.Context, p Principal) ([]domain.Session, error) {
	return s.Settings.ListSessions(ctx, p.UserID, s.Clock.Now())
}
func (s *Service) RevokeOwnSessions(ctx context.Context, p Principal, id *uuid.UUID) error {
	if id != nil && *id == p.SessionID {
		return apperr.Validation("id", "use_logout")
	}
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if id == nil {
			if err := s.Settings.RevokeOtherSessions(ctx, p.UserID, p.SessionID, s.Clock.Now()); err != nil {
				return err
			}
		} else {
			ok, err := s.Settings.RevokeOtherSession(ctx, p.UserID, p.SessionID, *id, s.Clock.Now())
			if err != nil {
				return err
			}
			if !ok {
				return apperr.NotFound("session not found")
			}
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "auth.revoke_sessions", ObjectType: "user", ObjectID: p.UserID.String(), ActorType: p.actorType()})
	})
	if err == nil {
		s.log.InfoContext(ctx, "sessions revoked", "op", "Account.RevokeOwnSessions")
	}
	return err
}
func (s *Service) emailAvailable(ctx context.Context, email string) error {
	_, err := s.Users.GetByEmail(ctx, email)
	if err == nil {
		return apperr.Conflict("email is already registered").WithDetail("reason", "email_taken")
	}
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}
func (s *Service) StartEmailChange(ctx context.Context, p Principal, email string) (*domain.EmailChange, error) {
	email = domain.NormalizeEmail(email)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return nil, apperr.Validation("new_email", "invalid")
	}
	var out *domain.EmailChange
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		u, err := s.Users.GetForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.Blocked() {
			return apperr.Forbidden("user_blocked")
		}
		count, err := s.Settings.CountEmailChanges(ctx, p.UserID, s.Clock.Now().Add(-time.Hour))
		if err != nil {
			return err
		}
		if count >= 3 {
			return apperr.RateLimited("email_change", time.Hour)
		}
		if err = s.emailAvailable(ctx, email); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err = s.Settings.ConsumeEmailChanges(ctx, p.UserID, now); err != nil {
			return err
		}
		out = &domain.EmailChange{ID: ids.New(), UserID: p.UserID, CurrentEmail: u.Email, NewEmail: email, SentAt: now, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), Sends: 1}
		if err = s.queueEmailChangeCodes(ctx, out); err != nil {
			return err
		}
		return s.Settings.InsertEmailChange(ctx, out)
	})
	return out, err
}
func (s *Service) queueEmailChangeCodes(ctx context.Context, c *domain.EmailChange) error {
	current, err := domain.GenerateCode()
	if err != nil {
		return err
	}
	next, err := domain.GenerateCode()
	if err != nil {
		return err
	}
	c.CurrentHash = c.Hash(s.Config.Pepper, "current", current)
	c.NewHash = c.Hash(s.Config.Pepper, "new", next)
	for _, v := range []struct{ email, code, purpose string }{{c.CurrentEmail, current, "current"}, {c.NewEmail, next, "new"}} {
		secret, err := json.Marshal(sealedLoginCode{Email: v.email, Code: v.code})
		if err != nil {
			return err
		}
		sealed, err := s.Sealer.Seal(secret)
		if err != nil {
			return err
		}
		if err = s.Jobs.Enqueue(ctx, job.Job{Kind: JobSendLoginCode, DedupeKey: "email_change:" + c.ID.String() + ":" + v.purpose + ":" + c.SentAt.Format(time.RFC3339Nano), Payload: loginCodePayload{AuthCodeID: c.ID, ExpiresAt: c.ExpiresAt, Sealed: sealed, Purpose: v.purpose, UserID: c.UserID, SentAt: c.SentAt}, MaxAttempts: sendLoginCodeAttempts}); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) ResendEmailChange(ctx context.Context, p Principal, id uuid.UUID) (*domain.EmailChange, error) {
	var out *domain.EmailChange
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		// Same lock order as confirm/start; serialize per user across challenges.
		u, err := s.Users.GetForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		c, err := s.Settings.LockEmailChange(ctx, p.UserID, id)
		if err != nil {
			return emailChallengeError(err)
		}
		now := s.Clock.Now()
		if c.ConsumedAt != nil {
			return apperr.Validation("code", "consumed_code")
		}
		if !now.Before(c.ExpiresAt) || u.Email != c.CurrentEmail {
			return apperr.Validation("code", "expired_code")
		}
		if c.Attempts >= 5 {
			return apperr.RateLimited("email_change_attempts", c.ExpiresAt.Sub(now))
		}
		if c.Sends >= 3 {
			return apperr.RateLimited("email_change_sends", c.ExpiresAt.Sub(now))
		}
		if next := c.SentAt.Add(time.Minute); now.Before(next) {
			return apperr.RateLimited("email_change_resend", next.Sub(now))
		}
		c.SentAt = now
		c.Sends++
		if err = s.queueEmailChangeCodes(ctx, c); err != nil {
			return err
		}
		out = c
		return s.Settings.SaveEmailChange(ctx, c)
	})
	return out, err
}
func emailChallengeError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("email change not found")
	}
	return err
}
func (s *Service) ConfirmEmailChange(ctx context.Context, p Principal, id uuid.UUID, currentCode, newCode string) error {
	snapshot, err := s.Settings.LockEmailChange(ctx, p.UserID, id)
	if err != nil {
		return emailChallengeError(err)
	}
	var failure error
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Settings.LockEmails(ctx, []string{snapshot.CurrentEmail, snapshot.NewEmail}); err != nil {
			return err
		}
		u, err := s.Users.GetForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		c, err := s.Settings.LockEmailChange(ctx, p.UserID, id)
		if err != nil {
			return emailChallengeError(err)
		}
		if c.ConsumedAt != nil {
			return apperr.Validation("code", "consumed_code")
		}
		if u.Email != c.CurrentEmail {
			return apperr.Validation("code", "expired_code")
		}
		failure = c.Verify(s.Config.Pepper, currentCode, newCode, s.Clock.Now())
		if err = s.Settings.SaveEmailChange(ctx, c); err != nil {
			return err
		}
		// Commit failed attempts instead of rolling them back with the HTTP error.
		if failure != nil {
			return nil
		}
		if err = s.Settings.ChangeEmail(ctx, p.UserID, c.NewEmail, s.Clock.Now()); err != nil {
			return err
		}
		if err = s.AuthCodes.ConsumeActive(ctx, c.CurrentEmail, s.Clock.Now()); err != nil {
			return err
		}
		if err = s.AuthCodes.ConsumeActive(ctx, c.NewEmail, s.Clock.Now()); err != nil {
			return err
		}
		if err = s.Settings.RevokeOtherSessions(ctx, p.UserID, p.SessionID, s.Clock.Now()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "auth.email_changed", ObjectType: "user", ObjectID: p.UserID.String(), ActorType: p.actorType()})
	})
	if err != nil {
		return err
	}
	if failure != nil {
		return failure
	}
	s.log.InfoContext(ctx, "account email changed", "op", "Account.ConfirmEmailChange")
	return nil
}
