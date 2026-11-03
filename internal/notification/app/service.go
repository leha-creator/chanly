// Package app — сценарии модуля уведомлений (07 §6): создание уведомления в транзакции
// события, отправка заданием send_notification ботом MAX и почтой, кабинет пользователя.
// Правила адресатов и каналов — в domain; внешние вызовы — через порты из обработчика задания.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// KindSendNotification — вид задания отправки (07 §2).
const KindSendNotification = "send_notification"

// Поля журнала модуля (07 §5.1).
const (
	KeyNotificationID = "notification_id"
	KeyTemplate       = "template"
	KeyChannel        = "channel"
)

const (
	defaultSendAttempts = 5
	defaultPageLimit    = 20
	maxPageLimit        = 100
)

// Config — параметры модуля.
type Config struct {
	// SendAttempts — max_attempts задания send_notification (07 §2: 5).
	SendAttempts int
}

func (c *Config) defaults() {
	if c.SendAttempts <= 0 {
		c.SendAttempts = defaultSendAttempts
	}
}

// Deps — зависимости сервиса.
type Deps struct {
	Tx         tx.Runner
	Jobs       job.Enqueuer
	Repo       domain.Repository
	Recipients domain.Recipients
	Channels   domain.Channels
	Renderer   domain.Renderer
	// Messenger и Mailer нужны только worker; в api — nil.
	Messenger domain.Messenger
	Mailer    domain.Mailer
	Clock     clock.Clock
	Log       *slog.Logger
	Config    Config
}

// Service — сценарии модуля уведомлений.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "notification")}
}

// SendPayload — полезная нагрузка send_notification: только идентификатор (почта адресата
// читается при отправке и в задание не попадает, 07 §4).
type SendPayload struct {
	NotificationID uuid.UUID `json:"notification_id"`
}

// DecodeSendPayload разбирает полезную нагрузку задания.
func DecodeSendPayload(raw []byte) (uuid.UUID, error) {
	var p SendPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.NotificationID == uuid.Nil {
		return uuid.Nil, job.Permanent(fmt.Errorf("send_notification: bad payload"))
	}
	return p.NotificationID, nil
}

// Notify создаёт уведомления события (R-1…R-4): в транзакции вызывающего, если она есть,
// иначе в своей. Строки кабинета доставлены сразу; для бота и почты ставится send_notification
// в той же транзакции (I-10). Ошибка — ошибка программы или базы: событие откатывается.
func (s *Service) Notify(ctx context.Context, n domain.Notice) error {
	const op = "Notification.Notify"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.enrich(ctx, op, &n); err != nil {
			return err
		}
		audience, err := s.audience(ctx, n)
		if err != nil {
			return err
		}
		rows, err := domain.Plan(n, audience, ids.New, s.Clock.Now())
		if err != nil {
			s.log.ErrorContext(ctx, "notification rejected", logx.KeyOp, op, KeyTemplate, n.Template, "role", n.Role,
				logx.KeyError, err.Error())
			return err
		}
		if len(rows) == 0 {
			s.log.DebugContext(ctx, "notification skipped", logx.KeyOp, op, KeyTemplate, n.Template, "role", n.Role,
				logx.KeyOrganizationID, n.OrganizationID, "reason", "no_recipients")
			return nil
		}
		inserted, err := s.Repo.Insert(ctx, rows)
		if err != nil {
			return err
		}
		if len(inserted) < len(rows) {
			s.log.DebugContext(ctx, "notification already created", logx.KeyOp, op, KeyTemplate, n.Template, "key", n.Key,
				"skipped", len(rows)-len(inserted))
		}
		var jobs []job.Job
		for _, r := range inserted {
			if r.Deliverable() {
				jobs = append(jobs, job.Job{Kind: KindSendNotification, DedupeKey: "notification:" + r.ID.String(),
					Payload: SendPayload{NotificationID: r.ID}, MaxAttempts: s.Config.SendAttempts})
			}
			s.log.InfoContext(ctx, "notification created", logx.KeyOp, op, KeyNotificationID, r.ID, KeyTemplate, r.Template,
				KeyChannel, r.Channel, logx.KeyUserID, r.UserID, logx.KeyOrganizationID, r.OrganizationID,
				"object_type", r.ObjectType, "object_id", r.ObjectID)
		}
		if len(jobs) > 0 {
			return s.Jobs.Enqueue(ctx, jobs...)
		}
		return nil
	})
}

// enrich дополняет данные названием канала (R-6); канал не найден — без названия.
func (s *Service) enrich(ctx context.Context, op string, n *domain.Notice) error {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	if _, ok := n.Data[domain.DataChannelTitle]; ok {
		return nil
	}
	id := channelID(n.Data)
	if id == nil {
		return nil
	}
	info, err := s.Channels.Channel(ctx, *id)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.WarnContext(ctx, "notification channel not found", logx.KeyOp, op, KeyTemplate, n.Template, "channel_id", *id)
		return nil
	}
	if err != nil {
		return err
	}
	if info.Title != "" {
		n.Data[domain.DataChannelTitle] = info.Title
	}
	return nil
}

// audience — адресаты по роли (R-1); блогеру — пользователь MAX канала (R-3).
func (s *Service) audience(ctx context.Context, n domain.Notice) (domain.Audience, error) {
	var a domain.Audience
	var err error
	if n.Role == domain.RoleAdmin {
		a.UserIDs, err = s.Recipients.Admins(ctx)
		return a, err
	}
	if n.OrganizationID == uuid.Nil {
		return a, fmt.Errorf("notification %s for %s without organization", n.Template, n.Role)
	}
	if a.UserIDs, err = s.Recipients.Members(ctx, n.OrganizationID); err != nil {
		return a, err
	}
	if n.Role != domain.RoleBlogger {
		return a, nil
	}
	id := channelID(n.Data)
	if id == nil {
		return a, nil
	}
	info, err := s.Channels.Channel(ctx, *id)
	if errors.Is(err, domain.ErrNotFound) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if info.AddedByMaxUserID != 0 {
		maxUser := info.AddedByMaxUserID
		a.MaxUserID = &maxUser
	}
	return a, nil
}

func channelID(data map[string]any) *uuid.UUID {
	switch v := data[domain.DataChannelID].(type) {
	case uuid.UUID:
		if v != uuid.Nil {
			return &v
		}
	case *uuid.UUID:
		return v
	case string:
		if id, err := uuid.Parse(v); err == nil {
			return &id
		}
	}
	return nil
}

func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

func notFound(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("notification not found")
	}
	return err
}
