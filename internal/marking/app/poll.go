package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// pollTarget — что опрашивать: снимок отправленного объекта из шага 1.
type pollTarget struct {
	requestID string
	sentAt    time.Time
}

// Poll — обработчик poll_ord_status (05 §3.5, R-10). Шаг 1 — объект ещё в sent; шаг 2 —
// GET /status вне транзакции; шаг 3 — статус применяется в транзакции. Следующий опрос —
// job.Postpone по расписанию; через ORD_STATUS_TIMEOUT без финального статуса — error.
func (s *Service) Poll(ctx context.Context, ref domain.ObjectRef) error {
	const op = "Marking.Poll"
	var target *pollTarget
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		r, err := s.lock(ctx, ref)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		st := r.state()
		if st.Status != domain.SyncSent {
			s.log.DebugContext(ctx, "ord object not awaiting status", logx.KeyOp, op, KeyObjectType, ref.Type,
				KeyObjectID, ref.ID, "sync_status", st.Status)
			return nil
		}
		target = &pollTarget{requestID: st.RequestID, sentAt: st.SentAt}
		return nil
	}); err != nil || target == nil {
		return err
	}
	if s.Ord == nil {
		return errors.New("marking: ORD adapter is not configured")
	}
	rec, callErr := s.Ord.GetStatus(ctx, target.requestID)
	var next time.Duration
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		next, err = s.applyPolled(ctx, ref, target, rec, callErr)
		return err
	}); err != nil {
		return err
	}
	if next > 0 {
		return job.Postpone(fmt.Errorf("ord status of %s is not final", ref), next)
	}
	return nil
}

// applyPolled — шаг 3 опроса. Возвращает задержку следующего опроса; 0 — опрос завершён.
func (s *Service) applyPolled(ctx context.Context, ref domain.ObjectRef, target *pollTarget, rec domain.Receipt,
	callErr error) (time.Duration, error) {
	const op = "Marking.Poll"
	r, err := s.lock(ctx, ref)
	if errors.Is(err, domain.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	st, now := r.state(), s.Clock.Now()
	if st.Status != domain.SyncSent || st.RequestID != target.requestID {
		return 0, nil // объект отправлен заново или переведён в другое состояние: опрос его не трогает
	}
	outcome := domain.OutcomeWaiting
	if callErr != nil {
		// Ошибка сети или ответа при опросе — следующий опрос по тому же расписанию (R-10).
		s.log.WarnContext(ctx, "ord status poll failed", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			KeyReqID, target.requestID, logx.KeyError, callErr.Error())
	} else {
		status, token, errs := rec.For(r.externalID())
		outcome = st.ApplyStatus(status, errs, domain.PassesErir(ref.Type), now)
		if err := s.afterStatus(ctx, r, outcome, token, now); err != nil {
			return 0, err
		}
	}
	delay, timeout := domain.NextPoll(target.sentAt, now, s.Config.Poll)
	if outcome == domain.OutcomeWaiting && timeout {
		st.Fail(domain.ReasonStatusTimeout, false)
		outcome = domain.OutcomeFailed
		s.log.ErrorContext(ctx, "ord status timeout", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			KeyReqID, target.requestID, KeyOrdStatus, st.OrdStatus)
		// erir_status креатива не меняется: ЕРИР не ответил, а не отклонил (R-12 не применяется).
	}
	if err := r.save(ctx, s, now); err != nil {
		return 0, err
	}
	switch outcome {
	case domain.OutcomeWaiting:
		s.log.DebugContext(ctx, "ord status polled", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			KeyReqID, target.requestID, KeyOrdStatus, st.OrdStatus, "next_poll_in", delay.String())
		return delay, nil
	case domain.OutcomeSynced:
		s.confirmed(ctx, op, r, now)
	case domain.OutcomeFailed:
		s.log.ErrorContext(ctx, "ord object rejected", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			KeyOrdStatus, st.OrdStatus, "errors_count", len(rec.Errors))
		s.failed(ctx, op, r, st.Error)
	}
	return 0, nil
}
