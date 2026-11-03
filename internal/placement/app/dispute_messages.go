package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Переписка по спору (R-9 этапа «API администратора»): администратор пишет стороне или обеим,
// сторона отвечает администратору. Текст в журнал приложения не пишется (07 §5.4).

// PostAdminMessage — сообщение администратора по спору адресату to; уведомление адресату.
func (s *Service) PostAdminMessage(ctx context.Context, disputeID, adminID uuid.UUID, to domain.MessageRecipient, text string) (domain.DisputeMessage, error) {
	const op = "Dispute.AdminMessage"
	reads, err := s.adminReads()
	if err != nil {
		return domain.DisputeMessage{}, err
	}
	d, err := reads.DisputeByID(ctx, disputeID)
	if err != nil {
		return domain.DisputeMessage{}, notFound(err, "dispute")
	}
	m, err := domain.NewAdminMessage(ids.New(), d, adminID, to, text, s.Clock.Now())
	if err != nil {
		s.log.WarnContext(ctx, "dispute message rejected", logx.KeyOp, op, "dispute_id", d.ID, "reason", rejectReason(err))
		return domain.DisputeMessage{}, ruleError(err)
	}
	roles := []domain.ActorType{domain.ActorAdvertiser, domain.ActorBlogger}
	switch to {
	case domain.RecipientAdvertiser:
		roles = roles[:1]
	case domain.RecipientBlogger:
		roles = roles[1:]
	}
	if err := s.saveMessage(ctx, op, d.PlacementID, m, roles); err != nil {
		return domain.DisputeMessage{}, err
	}
	return m, nil
}

// PostPartyMessage — ответ стороны side администратору по спору своего размещения.
func (s *Service) PostPartyMessage(ctx context.Context, side domain.MessageAuthor, orgID, userID, placementID uuid.UUID, text string) (domain.DisputeMessage, error) {
	const op = "Dispute.PartyMessage"
	d, err := s.partyDispute(ctx, side, orgID, placementID)
	if err != nil {
		return domain.DisputeMessage{}, err
	}
	m, err := domain.NewPartyMessage(ids.New(), d, side, userID, text, s.Clock.Now())
	if err != nil {
		s.log.WarnContext(ctx, "dispute message rejected", logx.KeyOp, op, "dispute_id", d.ID, "reason", rejectReason(err))
		return domain.DisputeMessage{}, ruleError(err)
	}
	if err := s.saveMessage(ctx, op, d.PlacementID, m, []domain.ActorType{domain.ActorAdmin}); err != nil {
		return domain.DisputeMessage{}, err
	}
	return m, nil
}

// PartyMessages — переписка, видимая стороне side.
func (s *Service) PartyMessages(ctx context.Context, side domain.MessageAuthor, orgID, placementID uuid.UUID) ([]domain.DisputeMessage, error) {
	d, err := s.partyDispute(ctx, side, orgID, placementID)
	if err != nil {
		return nil, err
	}
	all, err := s.Messages.List(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.DisputeMessage, 0, len(all))
	for _, m := range all {
		if m.VisibleTo(side) {
			out = append(out, m)
		}
	}
	return out, nil
}

// AdminMessages — вся переписка по спору.
func (s *Service) AdminMessages(ctx context.Context, disputeID uuid.UUID) ([]domain.DisputeMessage, error) {
	reads, err := s.adminReads()
	if err != nil {
		return nil, err
	}
	d, err := reads.DisputeByID(ctx, disputeID)
	if err != nil {
		return nil, notFound(err, "dispute")
	}
	return s.Messages.List(ctx, d.ID)
}

// partyDispute — спор размещения стороны: чужое размещение или нет спора — 404.
func (s *Service) partyDispute(ctx context.Context, side domain.MessageAuthor, orgID, placementID uuid.UUID) (domain.Dispute, error) {
	if s.Messages == nil {
		return domain.Dispute{}, errors.New("placement: dispute messages are not wired")
	}
	var err error
	switch side {
	case domain.AuthorAdvertiser:
		_, err = s.Placements.GetForAdvertiser(ctx, orgID, placementID)
	case domain.AuthorBlogger:
		_, err = s.Placements.GetForBlogger(ctx, orgID, placementID)
	default:
		return domain.Dispute{}, apperr.Forbidden("cabinet_required")
	}
	if err != nil {
		return domain.Dispute{}, notFound(err, "placement")
	}
	d, err := s.Disputes.GetByPlacement(ctx, placementID)
	if err != nil {
		return domain.Dispute{}, notFound(err, "dispute")
	}
	return d, nil
}

// saveMessage сохраняет сообщение и уведомления адресатам в одной транзакции.
func (s *Service) saveMessage(ctx context.Context, op string, placementID uuid.UUID, m domain.DisputeMessage, roles []domain.ActorType) error {
	p, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	o, err := s.Orders.GetByID(ctx, p.OrderID)
	if err != nil {
		return err
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Messages.Insert(ctx, m); err != nil {
			return err
		}
		for _, r := range roles {
			n := s.notice(o, p, domain.NoticeTo{Template: domain.NoticeDisputeMessage, Role: r,
				Data: map[string]any{"dispute_id": m.DisputeID, "message_id": m.ID}})
			if err := s.Notifier.Notify(ctx, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "dispute message posted", logx.KeyOp, op, "dispute_id", m.DisputeID, KeyPlacementID, placementID,
		"author_type", m.Author, "recipient", m.Recipient)
	return nil
}
