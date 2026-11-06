package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// outgoing — запрос к ОРД, собранный в шаге 1.
type outgoing struct {
	ref        domain.ObjectRef
	externalID string
	hash       string
	call       func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error)
}

// errDependencyWait — объект ждёт зависимость или изображения (R-4, R-9).
var errDependencyWait = errors.New("ord dependency is not ready")

// Sync — обработчик sync_ord_object (05 §3.2, I-13). Шаг 1 — транзакция: объект под блокировкой,
// зависимости (недостающие создаются вместе с заданиями), сборка запроса и хеша. Шаг 2 — вызов
// ОРД вне транзакции. Шаг 3 — транзакция: sent и опрос статуса, или error при отказе.
// attempt — номер попытки задания (для журнала).
func (s *Service) Sync(ctx context.Context, ref domain.ObjectRef, attempt int) error {
	var (
		out  *outgoing
		wait error
	)
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, wait, err = s.prepare(ctx, ref)
		return err
	}); err != nil {
		return err
	}
	if wait != nil {
		return wait // созданные зависимости и их задания уже зафиксированы
	}
	if out == nil {
		return nil
	}
	if s.Ord == nil {
		return errors.New("marking: ORD adapter is not configured")
	}
	rec, callErr := out.call(ctx, s.Ord)
	var result error
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		result, err = s.commit(ctx, out, rec, callErr, attempt)
		return err
	}); err != nil {
		return err
	}
	return result
}

// prepare — шаг 1. Возвращает запрос, ожидание (job.Postpone) или ничего, если отправлять не нужно.
func (s *Service) prepare(ctx context.Context, ref domain.ObjectRef) (*outgoing, error, error) {
	const op = "Marking.Sync"
	r, err := s.lock(ctx, ref)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.DebugContext(ctx, "ord object not found", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	st := r.state()
	if st.SubmissionUncertain {
		s.log.WarnContext(ctx, "ord report submission requires reconciliation", KeyObjectType, ref.Type, KeyObjectID, ref.ID)
		return nil, nil, nil
	}
	switch st.Status {
	case domain.SyncSent:
		s.log.DebugContext(ctx, "ord object up to date", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			"sync_status", st.Status)
		return nil, nil, nil
	case domain.SyncError:
		// Из error объект выводит только «Повторить» или новая нагрузка (R-2).
		s.log.DebugContext(ctx, "ord object in error, not sent", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID)
		return nil, nil, nil
	}
	out, wait, err := s.build(ctx, r)
	if err != nil {
		reason, ok := domain.BuildFailureReason(err)
		if !ok {
			return nil, nil, err
		}
		st.Fail(reason, false)
		if err := r.save(ctx, s, s.Clock.Now()); err != nil {
			return nil, nil, err
		}
		s.failed(ctx, op, r, reason)
		return nil, nil, nil
	}
	if wait != nil || out == nil {
		return nil, wait, nil
	}
	if st.UpToDate(out.hash) {
		s.log.DebugContext(ctx, "ord object up to date", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			"sync_status", st.Status)
		return nil, nil, nil
	}
	// build мог дополнить запись (дата договора, изначальный договор креатива).
	if isReportWrite(ref.Type) {
		st.SubmissionUncertain = true
		st.PayloadHash, st.ExternalID, st.SentAt = out.hash, out.externalID, s.Clock.Now()
		st.RequestID = ""
		st.Fail("submission_uncertain", false)
	}
	if err := r.save(ctx, s, s.Clock.Now()); err != nil {
		return nil, nil, err
	}
	return out, nil, nil
}

// build собирает запрос объекта по виду.
func (s *Service) build(ctx context.Context, r record) (*outgoing, error, error) {
	switch v := r.(type) {
	case *orgRecord:
		return s.buildOrganization(ctx, v)
	case *contractRecord:
		return s.buildContract(ctx, v)
	case *platformRecord:
		return s.buildPlatform(ctx, v)
	case *creativeRecord:
		return s.buildCreative(ctx, v)
	case *statisticsRecord:
		return s.buildStatistics(ctx, v)
	case *invoiceRecord:
		return s.buildInvoice(ctx, v)
	case *invoiceLinksRecord:
		return s.buildInvoiceLinks(ctx, v)
	}
	return nil, nil, fmt.Errorf("marking: unknown record %T", r)
}

// commit — шаг 3. Возвращает итог для планировщика (повторяемая ошибка вызова) отдельно от
// ошибки транзакции: счётчик попыток сохраняется и при повторе.
func (s *Service) commit(ctx context.Context, out *outgoing, rec domain.Receipt, callErr error, attempt int) (error, error) {
	const op = "Marking.Sync"
	r, err := s.lock(ctx, out.ref)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, now := r.state(), s.Clock.Now()
	if isReportWrite(out.ref.Type) && (!st.SubmissionUncertain || st.PayloadHash != out.hash) {
		return nil, errors.New("marking: report submission checkpoint changed")
	}
	st.ExternalID = out.externalID // и при отказе: по нему S-20 находит вызов в outbound_request
	if callErr != nil {
		var rejected interface{ DefiniteRejection() bool }
		definite := errors.As(callErr, &rejected) && rejected.DefiniteRejection()
		if job.IsPermanent(callErr) && (!isReportWrite(out.ref.Type) || definite) {
			st.SubmissionUncertain = false // definite rejection, no accepted receipt
			st.Fail(callErr.Error(), true)
			if err := r.save(ctx, s, now); err != nil {
				return nil, err
			}
			s.failed(ctx, op, r, callErr.Error())
			return nil, nil
		}
		st.Attempts++
		if err := r.save(ctx, s, now); err != nil {
			return nil, err
		}
		if isReportWrite(out.ref.Type) {
			s.failed(ctx, op, r, "submission_uncertain")
			return nil, nil
		}
		return callErr, nil
	}
	if isReportWrite(out.ref.Type) && rec.RequestID == "" {
		s.failed(ctx, op, r, "accepted_without_receipt")
		return nil, nil // durable checkpoint stays uncertain; no replay
	}
	status, token, errs := rec.For(out.externalID)
	st.MarkSent(out.externalID, rec.RequestID, status, out.hash, now)
	outcome := st.ApplyStatus(status, errs, domain.PassesErir(out.ref.Type), now)
	s.log.InfoContext(ctx, "ord object sent", logx.KeyOp, op, KeyObjectType, out.ref.Type, KeyObjectID, out.ref.ID,
		KeyReqID, rec.RequestID, KeyOrdStatus, status, "attempt", attempt)
	if err := s.afterStatus(ctx, r, outcome, token, now); err != nil {
		return nil, err
	}
	if err := r.save(ctx, s, now); err != nil {
		return nil, err
	}
	switch outcome {
	case domain.OutcomeWaiting:
		if err := s.enqueuePoll(ctx, out.ref, now.Add(s.Config.Poll.FastInterval)); err != nil {
			return nil, err
		}
	case domain.OutcomeFailed:
		s.failed(ctx, op, r, st.Error)
	case domain.OutcomeSynced:
		s.confirmed(ctx, op, r, now)
	}
	return nil, nil
}

func isReportWrite(t domain.ObjectType) bool {
	return t == domain.TypeStatistics || t == domain.TypeInvoice || t == domain.TypeInvoiceCreatives
}

// afterStatus — последствия нового статуса по виду объекта в той же транзакции: токен креатива,
// отклонение ЕРИР (R-8, R-12), готовность площадки (R-14).
func (s *Service) afterStatus(ctx context.Context, r record, o domain.Outcome, token string, now time.Time) error {
	switch v := r.(type) {
	case *creativeRecord:
		return s.creativeStatus(ctx, v, o, token, now)
	case *platformRecord:
		return s.platformStatus(ctx, v, now)
	}
	return nil
}

// SyncExhausted — sync_ord_object исчерпал попытки (OnExhausted): объект в error, оповещение.
func (s *Service) SyncExhausted(ctx context.Context, ref domain.ObjectRef, cause error) {
	const op = "Marking.SyncExhausted"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		r, err := s.lock(ctx, ref)
		if err != nil {
			return err
		}
		st := r.state()
		if st.Status != domain.SyncPending && st.Status != domain.SyncSynced {
			return nil
		}
		reason := domain.ReasonAttemptsExceeded
		if cause != nil {
			reason += ": " + cause.Error()
		}
		st.Fail(reason, false)
		if err := r.save(ctx, s, s.Clock.Now()); err != nil {
			return err
		}
		s.failed(ctx, op, r, reason)
		return nil
	})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.log.ErrorContext(ctx, "ord object exhaustion not recorded", logx.KeyOp, op, KeyObjectType, ref.Type,
			KeyObjectID, ref.ID, logx.KeyError, err.Error())
	}
}

// failed — объект в error: ERROR и оповещение ord_sync_failed (R-17). Тексты ошибок ОРД — только DEBUG.
func (s *Service) failed(ctx context.Context, op string, r record, reason string) {
	ref, st := r.ref(), r.state()
	s.log.ErrorContext(ctx, "ord object sync failed", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
		KeyOrdStatus, st.OrdStatus, "attempts", st.Attempts)
	s.log.DebugContext(ctx, "ord object error details", logx.KeyOp, op, KeyObjectID, ref.ID, "reason", reason)
	s.raise(ctx, op, alert.Alert{
		Reason: alertSyncFailed + string(ref.Type), ObjectKey: ref.ID.String(), Level: alert.LevelError,
		Summary: fmt.Sprintf("ORD %s %s is in error", ref.Type, ref.ID),
		Fields:  map[string]any{"ord_object_type": string(ref.Type), "ord_object_id": ref.ID.String(), "ord_status": st.OrdStatus},
	})
}

// confirmed — объект подтверждён (INFO, 07 §5.3).
func (s *Service) confirmed(ctx context.Context, op string, r record, now time.Time) {
	ref, st := r.ref(), r.state()
	s.log.InfoContext(ctx, "ord object confirmed", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
		KeyOrdStatus, st.OrdStatus, "elapsed_ms", now.Sub(st.SentAt).Milliseconds())
}

// wait — ожидание зависимости: DEBUG и job.Postpone без счёта попытки (R-4).
func (s *Service) wait(ctx context.Context, op string, ref domain.ObjectRef, v domain.Verdict) error {
	if v.Failed != nil {
		s.log.DebugContext(ctx, "ord dependency failed", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			"dependency", v.Failed.Need, "dependency_id", v.Failed.Ref.ID)
		return job.Postpone(fmt.Errorf("%w: %s in error", errDependencyWait, v.Failed.Need), s.Config.DependencyWaitFailed)
	}
	d := v.Waiting
	s.log.DebugContext(ctx, "ord dependency not ready", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
		"dependency", d.Need, "dependency_id", d.Ref.ID, KeyOrdStatus, d.State.OrdStatus)
	return job.Postpone(fmt.Errorf("%w: %s", errDependencyWait, d.Need), s.Config.DependencyWait)
}
