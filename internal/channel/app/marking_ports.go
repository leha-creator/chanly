package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// SetMarkingPorts подставляет модуль маркировки (этап «Маркировка», R-13; bootstrap).
// nil — маркировка выключена (ORD_ENABLED=false): одобренный канал сразу виден в каталоге (R-18).
func (s *Service) SetMarkingPorts(approval domain.ChannelApprovalListener) { s.approval = approval }

// SetOrdPlatformReady — площадка канала зарегистрирована в ОРД: канал становится виден в
// каталоге (R-14). Повтор ничего не меняет. Работает в транзакции вызывающего.
func (s *Service) SetOrdPlatformReady(ctx context.Context, channelID uuid.UUID, at time.Time) error {
	updated, err := s.Channels.SetOrdPlatformReady(ctx, channelID, at)
	if err != nil {
		return err
	}
	if updated {
		s.log.InfoContext(ctx, "channel ord platform ready", logx.KeyOp, "Channel.SetOrdPlatformReady", KeyChannelID, channelID)
	}
	return nil
}

// channelApproved — последствия одобрения для ОРД в транзакции одобрения (R-13, R-18).
func (s *Service) channelApproved(ctx context.Context, op string, c *domain.Channel) error {
	if s.approval == nil {
		return s.SetOrdPlatformReady(ctx, c.ID, s.Clock.Now())
	}
	s.log.DebugContext(ctx, "channel approval passed to marking", logx.KeyOp, op, KeyChannelID, c.ID)
	var org uuid.UUID
	if c.OrganizationID != nil {
		org = *c.OrganizationID
	}
	return s.approval.ChannelApproved(ctx, domain.ApprovedChannel{ID: c.ID, OrganizationID: org, Title: c.Title, Link: c.Link})
}
