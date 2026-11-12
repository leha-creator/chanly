package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Причины неудачной отправки в last_error: коды без персональных данных (07 §5.4).
const (
	reasonRenderFailed = "render_failed"
	reasonNoAddress    = "no_address"
	reasonRejected     = "rejected"
	reasonTemporary    = "temporary"
	reasonExhausted    = "attempts_exhausted"
	reasonNoAdapter    = "no_adapter"
)

// errUndeliverable — доставка невозможна без внешнего вызова (нет адреса, шаблон не собрался).
var errUndeliverable = errors.New("notification: undeliverable")

// Send — задание send_notification (07 §6, R-7): уведомление не pending — выход (повтор после
// успеха ничего не отправляет); текст и адрес — в момент отправки; внешний вызов — вне
// транзакции. Отказ без повтора — failed и задание завершается без ошибки: сбой доставки не
// требует человека и не влияет на событие. Временный сбой — попытка и повтор задания.
func (s *Service) Send(ctx context.Context, id uuid.UUID) error {
	const op = "Notification.Send"
	ctx = logx.With(ctx, KeyNotificationID, id.String())
	var n domain.Notification
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		n, err = s.Repo.GetForUpdate(ctx, id)
		return err
	}); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.log.WarnContext(ctx, "notification to send not found", logx.KeyOp, op)
			return nil
		}
		return err
	}
	if !n.Deliverable() {
		s.log.DebugContext(ctx, "notification already handled", logx.KeyOp, op, "status", n.Status, KeyChannel, n.Channel)
		return nil
	}
	allowed, err := s.deliveryPolicy(ctx, n)
	if err != nil {
		return err
	}
	if !allowed {
		return s.finish(ctx, id, func(n *domain.Notification) {
			n.Status = domain.StatusSuppressed
			n.UpdatedAt = s.Clock.Now()
			reason := "settings_disabled"
			n.LastError = &reason
		})
	}
	start := s.Clock.Now()
	s.log.DebugContext(ctx, "notification send started", logx.KeyOp, op, KeyTemplate, n.Template, KeyChannel, n.Channel,
		"attempt", n.Attempts+1)
	reason, err := s.deliver(ctx, n)
	switch {
	case err == nil:
		if err := s.finish(ctx, id, func(n *domain.Notification) { n.MarkSent(s.Clock.Now()) }); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "notification sent", logx.KeyOp, op, KeyTemplate, n.Template, KeyChannel, n.Channel,
			logx.KeyUserID, n.UserID, "elapsed_ms", s.Clock.Now().Sub(start).Milliseconds())
		return nil
	case errors.Is(err, errUndeliverable) || job.IsPermanent(err):
		if ferr := s.finish(ctx, id, func(n *domain.Notification) { n.MarkFailed(reason, s.Clock.Now()) }); ferr != nil {
			return ferr
		}
		// Отказ без повтора — ожидаемый исход (бот не может писать пользователю, Q-38): WARN.
		s.log.WarnContext(ctx, "notification failed", logx.KeyOp, op, KeyTemplate, n.Template, KeyChannel, n.Channel,
			logx.KeyUserID, n.UserID, "reason", reason, logx.KeyError, err.Error())
		return nil
	default:
		if ferr := s.finish(ctx, id, func(n *domain.Notification) { n.MarkAttempt(reason, s.Clock.Now()) }); ferr != nil {
			return ferr
		}
		s.log.WarnContext(ctx, "notification delivery failed", logx.KeyOp, op, KeyTemplate, n.Template, KeyChannel, n.Channel,
			"attempt", n.Attempts+1, "reason", reason, logx.KeyError, err.Error())
		return err
	}
}

// Exhausted — попытки send_notification исчерпаны (OnExhausted): уведомление failed (R-7).
func (s *Service) Exhausted(ctx context.Context, id uuid.UUID, cause error) {
	const op = "Notification.Exhausted"
	err := s.finish(ctx, id, func(n *domain.Notification) {
		n.MarkExhausted(reasonExhausted, s.Clock.Now())
	})
	if err != nil {
		s.log.ErrorContext(ctx, "notification state update failed", logx.KeyOp, op, KeyNotificationID, id, logx.KeyError, err.Error())
		return
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	s.log.ErrorContext(ctx, "notification failed", logx.KeyOp, op, KeyNotificationID, id, "reason", reasonExhausted, logx.KeyError, msg)
}

// finish меняет состояние уведомления под блокировкой, если оно ещё pending.
func (s *Service) finish(ctx context.Context, id uuid.UUID, change func(n *domain.Notification)) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		n, err := s.Repo.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if !n.Deliverable() {
			return nil
		}
		change(&n)
		return s.Repo.Update(ctx, n)
	})
}

// deliver собирает текст и отправляет; reason — код для last_error.
func (s *Service) deliver(ctx context.Context, n domain.Notification) (string, error) {
	switch n.Channel {
	case domain.ChannelMaxBot:
		if s.Messenger == nil {
			return reasonNoAdapter, fmt.Errorf("%w: no max messenger", errUndeliverable)
		}
		text, err := s.Renderer.MAX(n)
		if err != nil {
			return reasonRenderFailed, fmt.Errorf("%w: %v", errUndeliverable, err)
		}
		if err := s.Messenger.SendUserMessage(ctx, *n.MaxUserID, text); err != nil {
			return failReason(err), err
		}
		return "", nil
	case domain.ChannelEmail:
		if s.Mailer == nil {
			return reasonNoAdapter, fmt.Errorf("%w: no mailer", errUndeliverable)
		}
		to, err := s.Recipients.Email(ctx, n.UserID)
		if err != nil {
			return reasonTemporary, err
		}
		if to == "" {
			s.log.DebugContext(ctx, "notification skipped", logx.KeyOp, "Notification.Send", "reason", reasonNoAddress)
			return reasonNoAddress, fmt.Errorf("%w: user has no active email", errUndeliverable)
		}
		subject, text, html, err := s.Renderer.Email(n)
		if err != nil {
			return reasonRenderFailed, fmt.Errorf("%w: %v", errUndeliverable, err)
		}
		if err := s.Mailer.SendNotification(ctx, to, subject, text, html); err != nil {
			return failReason(err), err
		}
		return "", nil
	}
	return reasonNoAdapter, fmt.Errorf("%w: channel %s is not sent by job", errUndeliverable, n.Channel)
}

func failReason(err error) string {
	if job.IsPermanent(err) {
		return reasonRejected
	}
	return reasonTemporary
}
