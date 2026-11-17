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

func (s *Service) TelegramBotConnected(ctx context.Context, p Principal) (bool, error) {
	id, err := s.TelegramBot.Recipient(ctx, p.UserID)
	return id != 0, err
}
func (s *Service) CreateTelegramBotConnection(ctx context.Context, p Principal) (*domain.TelegramBotConnection, string, error) {
	bot := strings.TrimPrefix(s.Config.TelegramBotName, "@")
	if bot == "" || !s.Config.TelegramBotEnabled {
		return nil, "", apperr.New(apperr.CodeServiceUnavailable, "Telegram bot is not configured")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Clock.Now()
	c := &domain.TelegramBotConnection{ID: ids.New(), UserID: p.UserID, TokenHash: domain.HashTelegramBotToken(s.Config.Pepper, token), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		user, err := s.Users.GetForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if user.Blocked() {
			return apperr.Forbidden("user_blocked")
		}
		count, err := s.TelegramBot.CountConnections(ctx, p.UserID, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if count >= 5 {
			return apperr.RateLimited("telegram_bot_connection", time.Hour)
		}
		if err := s.TelegramBot.ExpireConnections(ctx, p.UserID); err != nil {
			return err
		}
		return s.TelegramBot.InsertConnection(ctx, c)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.DebugContext(ctx, "telegram notification connection created", "op", "Account.TelegramBot.Create")
	return c, "https://t.me/" + url.PathEscape(bot) + "?start=notify_" + token, nil
}
func (s *Service) TelegramBotConnection(ctx context.Context, p Principal, id uuid.UUID) (*domain.TelegramBotConnection, error) {
	c, err := s.TelegramBot.Connection(ctx, p.UserID, id, false)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("Telegram bot connection not found")
	}
	if err != nil {
		return nil, err
	}
	if c.Status == "pending" && !s.Clock.Now().Before(c.ExpiresAt) {
		c.Status = "expired"
	}
	return c, nil
}

// ConfirmTelegramBotConnection accepts only hashes derived by the authenticated Telegram webhook.
func (s *Service) ConfirmTelegramBotConnection(ctx context.Context, telegramUserID int64, hash string) error {
	if telegramUserID <= 0 || hash == "" {
		return nil
	}
	candidate, err := s.TelegramBot.ConnectionByHash(ctx, hash)
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
		c, err := s.TelegramBot.Connection(ctx, candidate.UserID, candidate.ID, true)
		if err != nil {
			return err
		}
		if c.Status != "pending" || !s.Clock.Now().Before(c.ExpiresAt) {
			return nil
		}
		if err := s.TelegramBot.Connect(ctx, c, telegramUserID, s.Clock.Now()); err != nil {
			return err
		}
		s.log.DebugContext(ctx, "telegram notification recipient connected", "op", "Account.TelegramBot.Confirm")
		return nil
	})
}
func (s *Service) DisconnectTelegramBot(ctx context.Context, p Principal) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Users.GetForUpdate(ctx, p.UserID); err != nil {
			return err
		}
		if err := s.TelegramBot.ExpireConnections(ctx, p.UserID); err != nil {
			return err
		}
		return s.TelegramBot.Disconnect(ctx, p.UserID)
	})
}
