package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func (s *Service) MAXBotConnected(ctx context.Context, p Principal) (bool, error) {
	id, err := s.MAXBot.Recipient(ctx, p.UserID)
	return id != 0, err
}
func (s *Service) CreateMAXBotConnection(ctx context.Context, p Principal) (*domain.MAXBotConnection, string, error) {
	bot := strings.TrimPrefix(s.Config.MAXBotName, "@")
	if bot == "" {
		return nil, "", apperr.New(apperr.CodeServiceUnavailable, "MAX bot is not configured")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Clock.Now()
	c := &domain.MAXBotConnection{ID: ids.New(), UserID: p.UserID, TokenHash: domain.HashMAXBotToken(s.Config.Pepper, token), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		user, err := s.Users.GetForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if user.Blocked() {
			return apperr.Forbidden("user_blocked")
		}
		count, err := s.MAXBot.CountConnections(ctx, p.UserID, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if count >= 5 {
			return apperr.RateLimited("max_bot_connection", time.Hour)
		}
		if err := s.MAXBot.ExpireConnections(ctx, p.UserID); err != nil {
			return err
		}
		return s.MAXBot.InsertConnection(ctx, c)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.DebugContext(ctx, "[FIX:max-bot] notification connection created", "op", "Account.MAXBot.Create")
	return c, "https://max.ru/" + url.PathEscape(bot) + "?start=notify_" + token, nil
}
func (s *Service) MAXBotConnection(ctx context.Context, p Principal, id uuid.UUID) (*domain.MAXBotConnection, error) {
	c, err := s.MAXBot.Connection(ctx, p.UserID, id, false)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("MAX bot connection not found")
	}
	if err != nil {
		return nil, err
	}
	if c.Status == "pending" && !s.Clock.Now().Before(c.ExpiresAt) {
		c.Status = "expired"
	}
	return c, nil
}

// ConfirmMAXBotConnection accepts only hashes derived by the authenticated MAX webhook.
func (s *Service) ConfirmMAXBotConnection(ctx context.Context, maxUserID int64, hash string) error {
	if maxUserID <= 0 || hash == "" {
		return nil
	}
	candidate, err := s.MAXBot.ConnectionByHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		user, err := s.Users.GetForUpdate(ctx, candidate.UserID)
		if err != nil {
			return err
		}
		if user.Blocked() {
			return nil
		}
		c, err := s.MAXBot.Connection(ctx, candidate.UserID, candidate.ID, true)
		if err != nil {
			return err
		}
		if c.Status != "pending" || !s.Clock.Now().Before(c.ExpiresAt) {
			return nil
		}
		if err := s.MAXBot.Connect(ctx, c, maxUserID, s.Clock.Now()); err != nil {
			return err
		}
		s.log.DebugContext(ctx, "[FIX:max-bot] notification recipient connected", "op", "Account.MAXBot.Confirm")
		return nil
	})
}
func (s *Service) DisconnectMAXBot(ctx context.Context, p Principal) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Users.GetForUpdate(ctx, p.UserID); err != nil {
			return err
		}
		if err := s.MAXBot.ExpireConnections(ctx, p.UserID); err != nil {
			return err
		}
		return s.MAXBot.Disconnect(ctx, p.UserID)
	})
}
