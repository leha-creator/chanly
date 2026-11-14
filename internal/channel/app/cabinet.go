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

// Update saves profile and optional replacement prices as one domain change.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p domain.ProfileUpdate) (*domain.Channel, error) {
	return s.saveBlogger(ctx, orgID, id, &p, p.Prices)
}

// saveBlogger is shared by PATCH and legacy PUT /prices. All durable effects
// use the transaction context; only a committed result is logged as success.
func (s *Service) saveBlogger(ctx context.Context, orgID, id uuid.UUID, p *domain.ProfileUpdate, prices *[]domain.Price) (*domain.Channel, error) {
	const op = "Channel.SaveBlogger"
	var out *domain.Channel
	var changed, priceChanges []string
	var change domain.StatusChange
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForOrganizationForUpdate(ctx, orgID, id)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		if err := c.CheckCatalogEditable(); err != nil {
			return notEditable(c)
		}
		// Lock the selected category until commit, serializing with admin archive.
		// Omitting category_id preserves an existing (possibly archived) relation.
		if p != nil && p.CategoryID != nil {
			category, err := s.Categories.GetForUpdate(ctx, *p.CategoryID)
			if errors.Is(err, domain.ErrNotFound) || (err == nil && category.Archived) {
				return apperr.Validation("category_id", "unknown category")
			}
			if err != nil {
				return err
			}
		}
		var formats []domain.Format
		if prices != nil {
			formats, err = s.Deps.Formats.ListActive(ctx)
			if err != nil {
				return err
			}
		}
		before, beforePrices := profile(c), priceMap(c.Prices)
		change = domain.StatusChange{ChannelID: c.ID, From: c.Status, To: c.Status}
		if p != nil {
			change, err = c.UpdateProfileWithPrices(*p, formats, s.Config.RknThreshold, s.Clock.Now(), s.Config.MinPriceAmount)
		} else {
			err = c.SetPricesWithMinimum(*prices, formats, s.Clock.Now(), s.Config.MinPriceAmount)
		}
		if errors.Is(err, domain.ErrNotEditable) {
			return notEditable(c)
		}
		if err != nil {
			return ruleError(err)
		}
		after, afterPrices := profile(c), priceMap(c.Prices)
		changed = diffKeys(before, after)
		priceChanges = diffKeys(beforePrices, afterPrices)
		if len(changed) == 0 && len(priceChanges) == 0 {
			out = c
			return nil
		}
		if prices != nil && len(priceChanges) > 0 {
			if err := s.Prices.Replace(ctx, c.ID, c.Prices, s.Clock.Now()); err != nil {
				return err
			}
		}
		if err := s.saveWithStatusLog(ctx, op, c, change, false); err != nil {
			return err
		}
		if p != nil && len(changed) > 0 {
			if err := s.Audit.Record(ctx, audit.Entry{Action: "channel.updated", ObjectType: "channel", ObjectID: c.ID.String(), Data: audit.Diff(before, after, changed), ActorType: audit.ActorBlogger}); err != nil {
				return err
			}
		}
		if prices != nil && len(priceChanges) > 0 {
			if err := s.Audit.Record(ctx, audit.Entry{Action: "channel.prices_updated", ObjectType: "channel", ObjectID: c.ID.String(), Data: audit.Diff(beforePrices, afterPrices, priceChanges), ActorType: audit.ActorBlogger}); err != nil {
				return err
			}
		}
		out = c
		return nil
	})
	if err != nil {
		if e, ok := apperr.As(err); ok {
			s.log.WarnContext(ctx, "channel save rejected", logx.KeyOp, op, KeyChannelID, id, "code", e.Code, "field", e.Details["field"], "reason", e.Details["reason"])
		} else {
			s.log.ErrorContext(ctx, "channel save transaction failed", logx.KeyOp, op, KeyChannelID, id)
		}
		return nil, err
	}
	if len(changed) > 0 || len(priceChanges) > 0 {
		if change.Changed() {
			s.logStatusChange(ctx, op, out, change)
		}
		fields := append([]string{}, changed...)
		if len(priceChanges) > 0 {
			fields = append(fields, "prices")
		}
		slices.Sort(fields)
		message := "channel updated"
		if p == nil {
			message = "[FIX] channel prices updated"
		}
		s.log.InfoContext(ctx, message, logx.KeyOp, op, KeyChannelID, id, "changed", fields, "status", out.Status, "enabled_count", len(out.EnabledFormats()))
	}
	return out, nil
}

func diffKeys(before, after map[string]any) []string {
	changed := []string{}
	for _, key := range unionKeys(before, after) {
		if before[key] != after[key] {
			changed = append(changed, key)
		}
	}
	return changed
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
