package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// IssuedLinkCode — код подключения для кабинета. Код показывается один раз и
// нигде не хранится в открытом виде.
type IssuedLinkCode struct {
	Code        string
	ExpiresAt   time.Time
	BotName     string
	Instruction string
}

// CreateLinkCode выдаёт организации код подключения канала (S-12 шаг 1, R-2).
func (s *Service) CreateLinkCode(ctx context.Context, orgID uuid.UUID) (IssuedLinkCode, error) {
	const op = "Channel.CreateLinkCode"
	now := s.Clock.Now()
	var issued IssuedLinkCode
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		n, err := s.LinkCodes.CountSince(ctx, orgID, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= s.Config.LinkCodesPerHour {
			s.log.WarnContext(ctx, "channel link code rate limited", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "count", n)
			return apperr.RateLimited("channel_link_code", time.Hour)
		}
		code, err := domain.GenerateLinkCode()
		if err != nil {
			return err
		}
		if err := s.LinkCodes.ConsumeLive(ctx, orgID, now); err != nil {
			return err
		}
		lc := domain.NewLinkCode(ids.New(), orgID, code, s.Config.Pepper, s.Config.LinkCodeTTL, now)
		if err := s.LinkCodes.Create(ctx, lc); err != nil {
			return err
		}
		// Сам код не пишется ни в журнал действий, ни в журнал приложения (07 §5.4).
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.link_code_created", ObjectType: "auth_code", ObjectID: lc.ID.String(),
			Data: map[string]any{"expires_at": lc.ExpiresAt}, ActorType: audit.ActorBlogger,
		}); err != nil {
			return err
		}
		issued = IssuedLinkCode{Code: code, ExpiresAt: lc.ExpiresAt, BotName: s.Config.BotName, Instruction: s.instruction(code)}
		s.log.InfoContext(ctx, "channel link code issued", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "expires_at", lc.ExpiresAt)
		return nil
	})
	return issued, err
}

// instruction — текст инструкции подключения (S-12 шаг 1).
func (s *Service) instruction(code string) string {
	bot := s.Config.BotName
	if bot == "" {
		bot = "бота платформы"
	}
	return fmt.Sprintf("Добавьте %s администратором канала с правами на публикацию и удаление сообщений, "+
		"затем отправьте в канал сообщение с кодом %s. Код действует %d минут. "+
		"Бот удалит сообщение с кодом и привяжет канал к вашему кабинету.",
		bot, code, int(s.Config.LinkCodeTTL.Minutes()))
}

// List — каналы организации.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*domain.Channel, error) {
	return s.Channels.ListByOrganization(ctx, orgID)
}

// Get — канал организации; not_found для чужого (07 §4).
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*domain.Channel, error) {
	c, err := s.Channels.GetForOrganization(ctx, orgID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("channel not found")
	}
	return c, err
}

// Update — правка описания, категории и номера РКН (S-12 шаг 5).
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p domain.ProfileUpdate) (*domain.Channel, error) {
	const op = "Channel.Update"
	if p.CategoryID != nil {
		ok, err := s.Categories.ExistsActive(ctx, *p.CategoryID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apperr.Validation("category_id", "unknown category")
		}
	}
	var out *domain.Channel
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForOrganizationForUpdate(ctx, orgID, id)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		before := profile(c)
		change, err := c.UpdateProfile(p, s.Config.RknThreshold, s.Clock.Now(), s.Config.Visibility)
		if errors.Is(err, domain.ErrNotEditable) {
			s.log.WarnContext(ctx, "channel update rejected", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
			return apperr.Conflict("channel cannot be edited in its current status").WithDetail("status", string(c.Status))
		}
		if err != nil {
			return err
		}
		after := profile(c)
		var changed []string
		for k, v := range after {
			if before[k] != v {
				changed = append(changed, k)
			}
		}
		slices.Sort(changed)
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.updated", ObjectType: "channel", ObjectID: c.ID.String(),
			Data: audit.Diff(before, after, changed), ActorType: audit.ActorBlogger,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel updated", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status, "changed", changed)
		out = c
		return nil
	})
	return out, err
}

func profile(c *domain.Channel) map[string]any {
	cat := ""
	if c.CategoryID != nil {
		cat = c.CategoryID.String()
	}
	return map[string]any{
		"description": c.Description, "category_id": cat, "rkn_number": c.RknNumber,
		"rkn_status": string(c.RknStatus), "status": string(c.Status),
	}
}

// Categories — справочник категорий каналов (R-13).
func (s *Service) ListCategories(ctx context.Context) ([]domain.Category, error) {
	return s.Categories.ListActive(ctx)
}
