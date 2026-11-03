package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ChannelPost — сообщение, опубликованное в канале (событие message_created).
type ChannelPost struct {
	MaxChatID int64
	MessageID string
	Text      string
	// SenderID — автор; 0 — пост от имени канала.
	SenderID int64
	// At — время сообщения по MAX.
	At time.Time
}

func deleteJob(chatID int64, messageID string) job.Job {
	return job.Job{
		Kind: KindDeleteMaxMessage, DedupeKey: "delete_message:" + messageID,
		Payload: DeletePayload{MaxChatID: chatID, MessageID: messageID}, MaxAttempts: DeleteMaxAttempts,
	}
}

// ChannelPostCreated — сообщение в канале (05 §2.2). Код подключения разбирается здесь
// (05 §2.4 шаг 3); остальные сообщения известного канала передаются модулю размещений —
// контроль «часов в топе» (R-9 этапа «Автопостинг и контроль»). Сообщение, разобранное как
// код, топ не нарушает: бот его удаляет.
func (s *Service) ChannelPostCreated(ctx context.Context, ev EventRef, p ChannelPost) error {
	const op = "Channel.LinkByCode"
	code, ok := domain.NormalizeLinkCode(p.Text)
	if !ok {
		return s.passPost(ctx, ev, p)
	}
	ctx = logx.With(ctx, "max_chat_id", p.MaxChatID)
	now := s.Clock.Now()
	notCode := false
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetByMaxChatIDForUpdate(ctx, p.MaxChatID)
		if errors.Is(err, domain.ErrNotFound) {
			// R-6: код мог прийти раньше, чем разобран bot_added.
			if now.Sub(ev.ReceivedAt) < s.Config.EarlyCodeWindow {
				s.log.DebugContext(ctx, "channel link code before bot_added, will retry", logx.KeyOp, op)
				return job.Retry(fmt.Errorf("channel for chat %d not known yet", p.MaxChatID), s.Config.EarlyCodeRetry)
			}
			s.log.WarnContext(ctx, "channel link code in unknown channel", logx.KeyOp, op)
			return ErrIgnored
		}
		if err != nil {
			return err
		}
		lc, err := s.LinkCodes.FindLiveForUpdate(ctx, domain.HashLinkCode(s.Config.Pepper, code), now)
		if errors.Is(err, domain.ErrNotFound) {
			// Неверный или просроченный код не обрабатывается (S-12, отклонения).
			s.log.WarnContext(ctx, "channel link code not found", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
			notCode = true
			return nil
		}
		if err != nil {
			return err
		}
		change, err := c.LinkTo(lc.OrganizationID, s.Config.RknThreshold, now, s.Config.Visibility)
		switch {
		case errors.Is(err, domain.ErrAlreadyLinked):
			lc.Consume(now)
			if err := s.LinkCodes.Consume(ctx, lc); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "channel already linked, code consumed", logx.KeyOp, op, KeyChannelID, c.ID)
			return s.Jobs.Enqueue(ctx, deleteJob(p.MaxChatID, p.MessageID))
		case errors.Is(err, domain.ErrLinkedToOther):
			return s.linkConflict(ctx, c, lc, p)
		case errors.Is(err, domain.ErrNotLinkable):
			s.log.WarnContext(ctx, "channel link code in released channel", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
			return nil
		case err != nil:
			return err
		}
		lc.Consume(now)
		if err := s.LinkCodes.Consume(ctx, lc); err != nil {
			return err
		}
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.linked", ObjectType: "channel", ObjectID: c.ID.String(),
			Data:           map[string]any{"max_chat_id": c.MaxChatID, "status": c.Status},
			ActorType:      audit.ActorSystem,
			OrganizationID: c.OrganizationID,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel linked", logx.KeyOp, op, KeyChannelID, c.ID,
			logx.KeyOrganizationID, lc.OrganizationID, "status", c.Status)
		return s.Jobs.Enqueue(ctx, deleteJob(p.MaxChatID, p.MessageID))
	})
	if err != nil || !notCode {
		return err
	}
	return s.passPost(ctx, ev, p)
}

// passPost передаёт сообщение известного канала модулю размещений (R-9). Неизвестный
// канал — ErrIgnored. Слушатель работает в своей транзакции: строка канала не блокируется.
func (s *Service) passPost(ctx context.Context, ev EventRef, p ChannelPost) error {
	const op = "Channel.ChannelPost"
	ctx = logx.With(ctx, "max_chat_id", p.MaxChatID)
	c, err := s.Channels.GetByMaxChatID(ctx, p.MaxChatID)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.DebugContext(ctx, "channel post in unknown chat", logx.KeyOp, op)
		return ErrIgnored
	}
	if err != nil {
		return err
	}
	eventID := ev.ID
	s.log.DebugContext(ctx, "channel post passed to placements", logx.KeyOp, op, KeyChannelID, c.ID, "message_id", p.MessageID)
	return s.PostListener.ChannelPostCreated(ctx, domain.ChannelPost{
		ChannelID: c.ID, MaxChatID: p.MaxChatID, MessageID: p.MessageID, SenderID: p.SenderID, At: p.At,
		InboundEventID: &eventID,
	})
}

// linkConflict — код другой организации в подключённом канале (R-10): владелец не
// меняется, код остаётся живым, сообщение удаляется, администратор оповещён.
func (s *Service) linkConflict(ctx context.Context, c *domain.Channel, lc *domain.LinkCode, p ChannelPost) error {
	const op = "Channel.LinkByCode"
	if err := s.Audit.Record(ctx, audit.Entry{
		Action: "channel.link_rejected", ObjectType: "channel", ObjectID: c.ID.String(),
		Data: map[string]any{
			"reason": "linked_to_other", "owner_organization_id": orgOf(c), "code_organization_id": lc.OrganizationID,
		},
		ActorType:      audit.ActorSystem,
		OrganizationID: c.OrganizationID,
	}); err != nil {
		return err
	}
	s.log.WarnContext(ctx, "channel link conflict", logx.KeyOp, op, KeyChannelID, c.ID,
		logx.KeyOrganizationID, orgOf(c), "code_organization_id", lc.OrganizationID)
	if s.Alerts != nil {
		if err := s.Alerts.Raise(ctx, alert.Alert{
			Reason: "channel_link_conflict", ObjectKey: c.ID.String(), Level: alert.LevelWarn,
			Summary: "Код подключения другой организации отправлен в уже подключённый канал",
			Fields:  map[string]any{"channel_id": c.ID, "max_chat_id": c.MaxChatID},
		}); err != nil {
			return err
		}
	}
	return s.Jobs.Enqueue(ctx, deleteJob(p.MaxChatID, p.MessageID))
}

// DeleteMessage — задание delete_max_message: удалить сообщение с кодом (05 §2.4 шаг 3).
// Без права delete удалить нельзя: это не повод повторять (канал уже pending_rights).
func (s *Service) DeleteMessage(ctx context.Context, p DeletePayload) error {
	ctx = logx.With(ctx, "max_chat_id", p.MaxChatID)
	return s.Bot.DeleteMessage(ctx, p.MaxChatID, p.MessageID)
}

// DialogMessage — пользователь написал боту в личном диалоге: ответ инструкцией
// (05 §2.2, D-25). Пока ответ не отправлен, новые сообщения второй не ставят.
func (s *Service) DialogMessage(ctx context.Context, userID int64) error {
	const op = "Channel.DialogMessage"
	if userID == 0 {
		return ErrIgnored
	}
	if err := s.Jobs.Enqueue(ctx, job.Job{
		Kind: KindReplyBotDialog, DedupeKey: "bot_dialog:" + strconv.FormatInt(userID, 10),
		Payload: ReplyPayload{MaxUserID: userID}, MaxAttempts: ReplyMaxAttempts,
		RunAt: s.Clock.Now().Add(dialogReplyDelay),
	}); err != nil {
		return err
	}
	s.log.DebugContext(ctx, "bot dialog reply scheduled", logx.KeyOp, op, "max_user_id", userID)
	return nil
}

// dialogReplyDelay — пауза перед ответом: несколько сообщений подряд получают один ответ.
const dialogReplyDelay = 5 * time.Second

// DialogReplyText — ответ бота в личном диалоге.
const DialogReplyText = "Здравствуйте! Это бот биржи рекламы в каналах MAX.\n\n" +
	"Чтобы продавать рекламу в своём канале, зарегистрируйтесь в кабинете блогера, " +
	"нажмите «Добавить канал» и следуйте инструкции: добавьте этого бота администратором канала " +
	"и отправьте в канал код подключения из кабинета.\n\nНа сообщения в этом чате бот не отвечает."

// ReplyDialog — задание reply_bot_dialog.
func (s *Service) ReplyDialog(ctx context.Context, userID int64) error {
	const op = "Channel.ReplyDialog"
	if _, err := s.Bot.SendUserMessage(ctx, userID, DialogReplyText); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "bot dialog replied", logx.KeyOp, op, "max_user_id", userID)
	return nil
}

// releaseBatch — сколько неподключённых каналов отпускается за один запуск.
const releaseBatch = 500

// ReleaseUnclaimed — задание release_unclaimed_channels (R-12): каналы, не подключённые
// кодом за UnclaimedTTL, получают disconnected, бот их покидает.
func (s *Service) ReleaseUnclaimed(ctx context.Context) error {
	const op = "Channel.ReleaseUnclaimed"
	now := s.Clock.Now()
	ids, err := s.Channels.ListUnclaimedBefore(ctx, now.Add(-s.Config.UnclaimedTTL), releaseBatch)
	if err != nil {
		return err
	}
	released := 0
	var lastErr error
	for _, id := range ids {
		err := s.Tx.InTx(ctx, func(ctx context.Context) error {
			c, err := s.Channels.GetForUpdate(ctx, id)
			if err != nil {
				return err
			}
			change, ok := c.ReleaseUnclaimed(now)
			if !ok {
				return nil // подключён, пока шёл запуск
			}
			if err := s.save(ctx, op, c, change); err != nil {
				return err
			}
			released++
			return s.Jobs.Enqueue(ctx, leaveJob(c.MaxChatID))
		})
		if err != nil {
			// Сбой одного канала не останавливает остальные; он повторится в следующий запуск.
			s.log.ErrorContext(ctx, "unclaimed channel release failed", logx.KeyOp, op, KeyChannelID, id, logx.KeyError, err.Error())
			lastErr = err
		}
	}
	s.log.InfoContext(ctx, "unclaimed channels released", logx.KeyOp, op, "count", released, "found", len(ids))
	return lastErr
}
