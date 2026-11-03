package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Unavailability — живые интервалы недоступности канала (прошедшие не показываются, R-6).
func (s *Service) Unavailability(ctx context.Context, channelID uuid.UUID) ([]domain.Unavailability, error) {
	return s.Unavail.ListLive(ctx, channelID, s.Clock.Now())
}

// AddUnavailability добавляет интервал недоступности (S-13, R-6). Проверка — под
// блокировкой канала: два одновременных запроса не создают пересечение.
func (s *Service) AddUnavailability(ctx context.Context, orgID, channelID uuid.UUID, u domain.Unavailability) (*domain.Channel, error) {
	const op = "Channel.AddUnavailability"
	var out *domain.Channel
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.editableChannel(ctx, op, orgID, channelID)
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		live, err := s.Unavail.ListLive(ctx, c.ID, now)
		if err != nil {
			return err
		}
		u.ID = ids.New()
		if err := domain.ValidateUnavailability(u, live, now, s.Config.Unavailability); err != nil {
			var re *domain.RuleError
			if errors.As(err, &re) {
				s.log.WarnContext(ctx, "channel unavailability rejected", logx.KeyOp, op, KeyChannelID, c.ID, "reason", re.Reason)
			}
			return ruleError(err)
		}
		// R-8 этапа «Каталог»: интервал не накрывает запланированное размещение. Блокировка
		// канала та же, под которой модуль размещений планирует публикации (R-14).
		conflict, err := s.Placements.ScheduledConflict(ctx, c.ID, u.StartsAt, u.EndsAt)
		if err != nil {
			return err
		}
		if conflict {
			s.log.WarnContext(ctx, "unavailability conflicts with placement", logx.KeyOp, op, KeyChannelID, c.ID)
			return ruleError(domain.ErrUnavailabilityPlacement)
		}
		if err := s.Unavail.Create(ctx, c.ID, u, now); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.unavailability_added", ObjectType: "channel", ObjectID: c.ID.String(),
			Data:      map[string]any{"unavailability_id": u.ID, "starts_at": u.StartsAt, "ends_at": u.EndsAt},
			ActorType: audit.ActorBlogger,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel unavailability added", logx.KeyOp, op, KeyChannelID, c.ID,
			"unavailability_id", u.ID, "starts_at", u.StartsAt, "ends_at", u.EndsAt)
		out = c
		return nil
	})
	return out, err
}

// DeleteUnavailability удаляет интервал своего канала; любой, в том числе прошедший (R-6).
func (s *Service) DeleteUnavailability(ctx context.Context, orgID, channelID, id uuid.UUID) (*domain.Channel, error) {
	const op = "Channel.DeleteUnavailability"
	var out *domain.Channel
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForOrganizationForUpdate(ctx, orgID, channelID)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		if err := s.Unavail.Delete(ctx, c.ID, id); errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("unavailability not found")
		} else if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.unavailability_deleted", ObjectType: "channel", ObjectID: c.ID.String(),
			Data: map[string]any{"unavailability_id": id}, ActorType: audit.ActorBlogger,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel unavailability deleted", logx.KeyOp, op, KeyChannelID, c.ID, "unavailability_id", id)
		out = c
		return nil
	})
	return out, err
}

// editableChannel — канал организации под блокировкой, в котором блогер правит цены и интервалы.
func (s *Service) editableChannel(ctx context.Context, op string, orgID, channelID uuid.UUID) (*domain.Channel, error) {
	c, err := s.Channels.GetForOrganizationForUpdate(ctx, orgID, channelID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("channel not found")
	}
	if err != nil {
		return nil, err
	}
	if err := c.CheckCatalogEditable(); err != nil {
		s.log.WarnContext(ctx, "channel unavailability rejected", logx.KeyOp, op, KeyChannelID, c.ID, "reason", "not_editable", "status", c.Status)
		return nil, notEditable(c)
	}
	return c, nil
}
