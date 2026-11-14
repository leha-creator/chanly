package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Блокировка каналов и справочник категорий (R-7, R-8 этапа «API администратора»). Роль senior
// проверяет слой HTTP.

// Block — администратор блокирует канал: нет в каталоге и корзине, новые размещения не создаются;
// начатые продолжаются (S-25 не применяется). Основание — в audit_log, блогер уведомлён.
func (s *Service) Block(ctx context.Context, adminID, id uuid.UUID, reason string) (*AdminChannelView, error) {
	const op = "Channel.Block"
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	err := s.blockTx(ctx, op, id, func(c *domain.Channel) (domain.StatusChange, error) { return c.Block(s.Clock.Now()) },
		"channel.blocked", reason)
	if err != nil {
		return nil, err
	}
	s.log.WarnContext(ctx, "admin channel blocked", logx.KeyOp, op, KeyChannelID, id, "admin_user_id", adminID)
	return s.AdminGet(ctx, id)
}

// Unblock — снятие блокировки: канал снова на модерации (без прав — pending_rights).
func (s *Service) Unblock(ctx context.Context, adminID, id uuid.UUID, reason string) (*AdminChannelView, error) {
	const op = "Channel.Unblock"
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.Validation("reason", "required")
	}
	err := s.blockTx(ctx, op, id, func(c *domain.Channel) (domain.StatusChange, error) {
		return c.Unblock(s.Config.RknThreshold, s.Clock.Now())
	}, "channel.unblocked", reason)
	if err != nil {
		return nil, err
	}
	s.log.WarnContext(ctx, "admin channel unblocked", logx.KeyOp, op, KeyChannelID, id, "admin_user_id", adminID)
	return s.AdminGet(ctx, id)
}

// blockTx — канал под блокировкой, правило domain, сохранение, audit_log и уведомление блогеру.
// Смена доступности (ChannelUnavailable) не вызывается: начатые размещения продолжаются (R-7).
func (s *Service) blockTx(ctx context.Context, op string, id uuid.UUID, decide func(c *domain.Channel) (domain.StatusChange, error),
	action, reason string) error {
	ctx = audit.WithActor(ctx, adminActor(ctx))
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Admin.GetForAdminForUpdate(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		change, err := decide(c)
		switch {
		case errors.Is(err, domain.ErrAlreadyBlocked):
			s.log.WarnContext(ctx, "admin channel block rejected", logx.KeyOp, op, KeyChannelID, c.ID, "reason", "already_blocked")
			return apperr.Conflict("channel is already blocked").WithDetail("reason", "already_blocked")
		case errors.Is(err, domain.ErrNotBlocked):
			s.log.WarnContext(ctx, "admin channel unblock rejected", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
			return apperr.Conflict("channel is not blocked").WithDetail("status", string(c.Status))
		case err != nil:
			return err
		}
		if err := s.Channels.Save(ctx, c); err != nil {
			if errors.Is(err, domain.ErrStaleVersion) {
				return apperr.Conflict("channel was changed concurrently").WithCause(err)
			}
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{Action: action, ObjectType: "channel", ObjectID: c.ID.String(),
			ActorType: audit.ActorAdmin, Reason: &reason, OrganizationID: c.OrganizationID,
			Data: map[string]any{"from": change.From, "to": change.To}}); err != nil {
			return err
		}
		return s.notify(ctx, c, change)
	})
}

// ListAdminCategories returns the complete dictionary for the senior-only HTTP route.
func (s *Service) ListAdminCategories(ctx context.Context) ([]domain.Category, error) {
	list, err := s.Categories.ListAll(ctx)
	if err == nil {
		s.log.DebugContext(ctx, "admin channel categories listed", logx.KeyOp, "Channel.ListAdminCategories", "role", "senior", "count", len(list))
	}
	return list, err
}

// CreateCategory creates an active category; archived slugs remain reserved.
func (s *Service) CreateCategory(ctx context.Context, adminID uuid.UUID, in domain.CategoryInput) (domain.Category, error) {
	const op = "Channel.CreateCategory"
	if err := domain.CheckSlug(in.Slug); err != nil {
		return domain.Category{}, ruleError(err)
	}
	if err := domain.CheckCategoryName(in.Name); err != nil {
		return domain.Category{}, ruleError(err)
	}
	c := domain.Category{ID: ids.New(), Slug: in.Slug, Name: strings.TrimSpace(in.Name)}
	ctx = audit.WithActor(ctx, adminActor(ctx))
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if in.Position != nil {
			c.Position = *in.Position
		} else {
			n, err := s.Categories.NextPosition(ctx)
			if err != nil {
				return err
			}
			c.Position = n
		}
		if err := s.Categories.Insert(ctx, c, s.Clock.Now()); err != nil {
			if errors.Is(err, domain.ErrCategoryExists) {
				s.log.WarnContext(ctx, "channel category rejected", logx.KeyOp, op, "reason", "slug_taken", "slug", c.Slug)
				return apperr.Conflict("category slug already exists").WithDetail("reason", "slug_taken")
			}
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "channel_category.created", ObjectType: "channel_category",
			ObjectID: c.ID.String(), ActorType: audit.ActorAdmin, Data: map[string]any{"slug": c.Slug, "name": c.Name,
				"position": c.Position}})
	})
	if err != nil {
		return domain.Category{}, err
	}
	s.log.WarnContext(ctx, "channel category changed", logx.KeyOp, op, "category_id", c.ID, "slug", c.Slug, "admin_user_id", adminID)
	return c, nil
}

// UpdateCategory — правка названия, позиции или архива категории (R-8). Архивная не видна
// кабинетам; каналы с ней остаются.
func (s *Service) UpdateCategory(ctx context.Context, adminID, id uuid.UUID, in domain.CategoryInput) (domain.Category, error) {
	const op = "Channel.UpdateCategory"
	if in.Name != "" {
		if err := domain.CheckCategoryName(in.Name); err != nil {
			return domain.Category{}, ruleError(err)
		}
	}
	var c domain.Category
	ctx = audit.WithActor(ctx, adminActor(ctx))
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		c, err = s.Categories.GetForUpdate(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("category not found")
		}
		if err != nil {
			return err
		}
		before := c
		if in.Name != "" {
			c.Name = strings.TrimSpace(in.Name)
		}
		if in.Position != nil {
			c.Position = *in.Position
		}
		if in.Archived != nil {
			c.Archived = *in.Archived
		}
		if err := s.Categories.Update(ctx, c, s.Clock.Now()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "channel_category.updated", ObjectType: "channel_category",
			ObjectID: c.ID.String(), ActorType: audit.ActorAdmin, Data: audit.Diff(categoryMap(before), categoryMap(c),
				[]string{"name", "position", "archived"})})
	})
	if err != nil {
		return domain.Category{}, err
	}
	s.log.WarnContext(ctx, "channel category changed", logx.KeyOp, op, "category_id", c.ID, "archived", c.Archived,
		"admin_user_id", adminID)
	return c, nil
}

func categoryMap(c domain.Category) map[string]any {
	return map[string]any{"name": c.Name, "position": c.Position, "archived": c.Archived}
}

// ModerationCount — каналы на модерации (счётчик разделов администратора, R-16).
func (s *Service) ModerationCount(ctx context.Context) (int, error) {
	if s.Counter == nil {
		return 0, nil
	}
	return s.Counter.ModerationCount(ctx)
}
