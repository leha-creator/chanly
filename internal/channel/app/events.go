package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Виды заданий модуля (R-16). Внешние вызовы MAX — только в них, вне транзакции (R-7).
const (
	KindRefreshChannelInfo       = "refresh_channel_info"
	KindDeleteMaxMessage         = "delete_max_message"
	KindLeaveMaxChat             = "leave_max_chat"
	KindReplyBotDialog           = "reply_bot_dialog"
	KindReleaseUnclaimedChannels = "release_unclaimed_channels"
)

// Пределы попыток (R-16).
const (
	RefreshMaxAttempts = 5
	DeleteMaxAttempts  = 5
	LeaveMaxAttempts   = 5
	ReplyMaxAttempts   = 3
)

// ErrIgnored — событие не требует действий; приём отмечает его ignored (05 §2.2).
var ErrIgnored = errors.New("channel: event ignored")

// RefreshPayload — полезная нагрузка refresh_channel_info.
type RefreshPayload struct {
	ChannelID uuid.UUID `json:"channel_id"`
}

// DeletePayload — полезная нагрузка delete_max_message.
type DeletePayload struct {
	MaxChatID int64  `json:"max_chat_id"`
	MessageID string `json:"message_id"`
}

// LeavePayload — полезная нагрузка leave_max_chat.
type LeavePayload struct {
	MaxChatID int64 `json:"max_chat_id"`
}

// ReplyPayload — полезная нагрузка reply_bot_dialog.
type ReplyPayload struct {
	MaxUserID int64 `json:"max_user_id"`
}

// EventRef — событие, из которого выполняется сценарий: ключи заданий строятся
// от него, чтобы повтор разбора не ставил задания второй раз.
type EventRef struct {
	ID         uuid.UUID
	ReceivedAt time.Time
}

func (s *Service) refreshJob(channelID uuid.UUID, ev EventRef) job.Job {
	return job.Job{
		Kind: KindRefreshChannelInfo, DedupeKey: "channel_info:" + channelID.String() + ":" + ev.ID.String(),
		Payload: RefreshPayload{ChannelID: channelID}, MaxAttempts: RefreshMaxAttempts,
	}
}

func leaveJob(chatID int64) job.Job {
	id := strconv.FormatInt(chatID, 10)
	return job.Job{Kind: KindLeaveMaxChat, DedupeKey: "leave_chat:" + id, Payload: LeavePayload{MaxChatID: chatID}, MaxAttempts: LeaveMaxAttempts}
}

// BotAdded — событие bot_added (05 §2.4 шаги 1–2, R-9). Канал создаётся в unclaimed
// или восстанавливается; права читает задание refresh_channel_info. Бот, добавленный
// не в канал, покидает чат.
func (s *Service) BotAdded(ctx context.Context, ev EventRef, chatID, addedBy int64, isChannel bool) error {
	const op = "Channel.BotAdded"
	ctx = logx.With(ctx, "max_chat_id", chatID)
	if !isChannel {
		if err := s.Jobs.Enqueue(ctx, leaveJob(chatID)); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "max chat is not a channel, leaving", logx.KeyOp, op)
		return nil
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		c, err := s.Channels.GetByMaxChatIDForUpdate(ctx, chatID)
		if errors.Is(err, domain.ErrNotFound) {
			c = domain.NewChannel(ids.New(), chatID, addedBy, now)
			if err := s.Channels.Create(ctx, c); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "channel bot added", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status, "created", true)
			return s.Jobs.Enqueue(ctx, s.refreshJob(c.ID, ev))
		}
		if err != nil {
			return err
		}
		prevAddedBy := c.AddedByMaxUserID
		change := c.BotAdded(addedBy, now)
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		if c.AddedByMaxUserID != addedBy && prevAddedBy != addedBy {
			// Бот снова добавлен в подключённый канал другим пользователем: владелец и
			// адресат сообщений не меняются (R-9).
			s.log.WarnContext(ctx, "channel bot added by another user, owner kept", logx.KeyOp, op, KeyChannelID, c.ID,
				logx.KeyOrganizationID, orgOf(c))
		}
		s.log.InfoContext(ctx, "channel bot added", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status, "created", false)
		return s.Jobs.Enqueue(ctx, s.refreshJob(c.ID, ev))
	})
}

// BotRemoved — событие bot_removed (S-25 шаг 1). Неизвестный канал — ErrIgnored.
func (s *Service) BotRemoved(ctx context.Context, chatID int64) error {
	const op = "Channel.BotRemoved"
	ctx = logx.With(ctx, "max_chat_id", chatID)
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetByMaxChatIDForUpdate(ctx, chatID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.DebugContext(ctx, "bot removed from unknown chat", logx.KeyOp, op)
			return ErrIgnored
		}
		if err != nil {
			return err
		}
		change := c.BotRemoved(s.Clock.Now())
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel bot removed", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
		return nil
	})
}

// RightsChanged — событие bot_admin_permissions_changed (05 §2.4 шаг 7): права
// перечитываются заданием, одно место расчёта состояния.
func (s *Service) RightsChanged(ctx context.Context, ev EventRef, chatID, actorID int64, isChannel bool) error {
	const op = "Channel.RightsChanged"
	ctx = logx.With(ctx, "max_chat_id", chatID)
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetByMaxChatIDForUpdate(ctx, chatID)
		if errors.Is(err, domain.ErrNotFound) {
			if !isChannel {
				return ErrIgnored
			}
			// MAX can grant channel admin rights without sending bot_added.
			// Discover an unclaimed channel; only a live code can assign its owner.
			c = domain.NewChannel(ids.New(), chatID, actorID, s.Clock.Now())
			if err := s.Channels.Create(ctx, c); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "[FIX] channel discovered from rights event", logx.KeyOp, op, KeyChannelID, c.ID)
			return s.Jobs.Enqueue(ctx, s.refreshJob(c.ID, ev))
		}
		if err != nil {
			return err
		}
		s.log.DebugContext(ctx, "channel rights refresh requested", logx.KeyOp, op, KeyChannelID, c.ID)
		return s.Jobs.Enqueue(ctx, s.refreshJob(c.ID, ev))
	})
}

// TitleChanged — событие chat_title_changed. Неизвестный канал — ErrIgnored.
func (s *Service) TitleChanged(ctx context.Context, chatID int64, title string) error {
	const op = "Channel.TitleChanged"
	ctx = logx.With(ctx, "max_chat_id", chatID)
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetByMaxChatIDForUpdate(ctx, chatID)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrIgnored
		}
		if err != nil {
			return err
		}
		c.TitleChanged(title, s.Clock.Now())
		if err := s.save(ctx, op, c, domain.StatusChange{From: c.Status, To: c.Status}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel title updated", logx.KeyOp, op, KeyChannelID, c.ID)
		return nil
	})
}

// RefreshChannelInfo — задание refresh_channel_info: сведения и права из MAX вне
// транзакции, затем состояние в транзакции (05 §2.4 шаги 4–6).
func (s *Service) RefreshChannelInfo(ctx context.Context, channelID uuid.UUID) error {
	const op = "Channel.RefreshChannelInfo"
	ctx = logx.With(ctx, KeyChannelID, channelID.String())
	var chatID int64
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForUpdate(ctx, channelID)
		if err != nil {
			return err
		}
		chatID = c.MaxChatID
		if c.Status == domain.StatusDisconnected {
			chatID = 0
		}
		return nil
	})
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(fmt.Errorf("channel %s not found", channelID))
	}
	if err != nil {
		return err
	}
	if chatID == 0 {
		s.log.DebugContext(ctx, "channel disconnected, rights refresh skipped", logx.KeyOp, op)
		return nil
	}
	info, err := s.Chats.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	m, err := s.Chats.GetMembership(ctx, chatID)
	if err != nil {
		return err
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForUpdate(ctx, channelID)
		if err != nil {
			return err
		}
		if c.Status == domain.StatusDisconnected {
			// bot_removed пришёл, пока шло чтение: прочитанные права устарели.
			s.log.DebugContext(ctx, "channel disconnected during refresh, result dropped", logx.KeyOp, op)
			return nil
		}
		change := c.RightsRefreshed(info, m, s.Config.RknThreshold, s.Clock.Now())
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		missing := c.MissingPermissions()
		s.log.InfoContext(ctx, "channel rights refreshed", logx.KeyOp, op, "status", c.Status,
			"bot_is_admin", c.BotIsAdmin, "missing", missing, "is_public", c.IsPublic, "subscribers_count", c.SubscribersCount)
		if len(missing) > 0 && c.OrganizationID != nil {
			s.log.WarnContext(ctx, "channel rights lost", logx.KeyOp, op, "missing", missing, "status", c.Status)
		}
		return nil
	})
}

// LeaveChat — задание leave_max_chat.
func (s *Service) LeaveChat(ctx context.Context, chatID int64) error {
	ctx = logx.With(ctx, "max_chat_id", chatID)
	return s.Bot.LeaveChat(ctx, chatID)
}
