package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// DevChannelInput — канал maxim dev seed: сведения, которые обычно приходят из MAX.
type DevChannelInput struct {
	// MaxChatID — идентификатор чата из диапазона dev seed; по нему повтор находит канал.
	MaxChatID   int64
	Title       string
	Description string
	Link        string
	Subscribers int
}

// DevSeedChannel создаёт канал организации orgID таким, каким он становится после кода
// подключения и чтения прав: публичный, бот с правами write и delete, состояние moderation.
// Только для maxim dev seed (APP_ENV development/test): без MAX, заданий и событий бота.
// Дальше канал проходит обычные Update, SetPrices и Approve. Повтор по MaxChatID возвращает
// существующий канал с created = false.
func (s *Service) DevSeedChannel(ctx context.Context, orgID uuid.UUID, in DevChannelInput) (*domain.Channel, bool, error) {
	const op = "Channel.DevSeedChannel"
	if in.MaxChatID == 0 {
		return nil, false, apperr.Validation("max_chat_id", "required")
	}
	var (
		c       *domain.Channel
		created bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		got, err := s.Channels.GetByMaxChatID(ctx, in.MaxChatID)
		if err == nil {
			if got.OrganizationID == nil || *got.OrganizationID != orgID {
				return apperr.Conflict("dev channel belongs to another organization").WithDetail("max_chat_id", in.MaxChatID)
			}
			c = got
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		now := s.Clock.Now()
		c = domain.NewChannel(ids.New(), in.MaxChatID, 0, now)
		if _, err := c.LinkTo(orgID, s.Config.RknThreshold, now, s.Config.Visibility); err != nil {
			return err
		}
		c.RightsRefreshed(domain.ChatInfo{
			Title: in.Title, Description: in.Description, Link: in.Link,
			IsPublic: true, IsChannel: true, SubscribersCount: in.Subscribers,
		}, domain.Membership{IsAdmin: true, Permissions: domain.RequiredPermissions}, s.Config.RknThreshold, now, s.Config.Visibility)
		if c.Status != domain.StatusModeration {
			return apperr.Validation("subscribers", c.StatusReason)
		}
		created = true
		return s.Channels.Create(ctx, c)
	})
	if err != nil {
		return nil, false, err
	}
	s.log.InfoContext(ctx, "dev channel seeded", logx.KeyOp, op, KeyChannelID, c.ID, "max_chat_id", c.MaxChatID,
		"status", c.Status, "created", created)
	return c, created, nil
}
