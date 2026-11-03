package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ErrIgnored — событие не относится к публикациям; приём отмечает его ignored (05 §2.2).
var ErrIgnored = errors.New("placement: event ignored")

// ChannelPost — сообщение в канале (message_created, S-27, R-9): чужое сообщение во время
// «часов в топе» — нарушение top_violated. Вызывается модулем каналов вне его транзакции.
func (s *Service) ChannelPost(ctx context.Context, post domain.ChannelPost) error {
	const op = "Placement.ChannelPost"
	ctx = logx.With(ctx, KeyChannelID, post.ChannelID.String())
	live, err := s.Publications.ListLiveByChannel(ctx, post.ChannelID)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		s.log.DebugContext(ctx, "channel post ignored", logx.KeyOp, op, "reason", "no_live_publications")
		return nil
	}
	bot, err := s.Publisher.BotUserID(ctx)
	if err != nil {
		return err
	}
	found := domain.ClassifyChannelPost(live, post, bot)
	if len(found) == 0 {
		reason := "outside_top"
		if post.SenderID != 0 && post.SenderID == bot {
			reason = "own_bot"
		}
		s.log.DebugContext(ctx, "channel post ignored", logx.KeyOp, op, "reason", reason, "message_id", post.MessageID)
		return nil
	}
	for _, v := range found {
		if err := s.reportViolation(ctx, op, v, source{InboundEventID: post.InboundEventID}); err != nil {
			return err
		}
	}
	return nil
}

// MessageEdited — событие message_edited по сообщению (S-26): содержание публикации изменено.
// Сообщение не наша публикация — ErrIgnored.
func (s *Service) MessageEdited(ctx context.Context, eventID uuid.UUID, messageID, hash string, at time.Time) error {
	const op = "Placement.MessageEdited"
	pub, ok, err := s.Publications.FindByMessageID(ctx, messageID)
	if err != nil {
		return err
	}
	if !ok {
		s.log.DebugContext(ctx, "message event for unknown publication", logx.KeyOp, op, "max_message_id", messageID)
		return ErrIgnored
	}
	v, found := domain.ClassifyEdited(pub, hash, at)
	if !found {
		s.log.DebugContext(ctx, "message edit without content change", logx.KeyOp, op, "publication_id", pub.ID)
		return nil
	}
	v.InboundEventID = &eventID
	return s.reportViolation(ctx, op, v, source{InboundEventID: &eventID})
}

// MessageRemoved — событие message_removed по сообщению (S-26): своё удаление записано
// намерением заранее (05 §2.2); чужое — досрочное снятие. Не наша публикация — ErrIgnored.
func (s *Service) MessageRemoved(ctx context.Context, eventID uuid.UUID, messageID string, at time.Time) error {
	const op = "Placement.MessageRemoved"
	pub, ok, err := s.Publications.FindByMessageID(ctx, messageID)
	if err != nil {
		return err
	}
	if !ok {
		s.log.DebugContext(ctx, "message event for unknown publication", logx.KeyOp, op, "max_message_id", messageID)
		return ErrIgnored
	}
	v, found := domain.ClassifyRemoved(pub, at)
	if !found {
		s.log.DebugContext(ctx, "repeated event ignored", logx.KeyOp, op, "publication_id", pub.ID, "reason", "own_removal")
		return nil
	}
	v.InboundEventID = &eventID
	return s.reportViolation(ctx, op, v, source{InboundEventID: &eventID})
}
