package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Страница списка ошибок.
const (
	errorsDefaultLimit = 50
	errorsMaxLimit     = 500
	actionOrdRetry     = "ord.retry"
)

// ErrorPage — страница объектов ОРД в error (S-20); NextCursor пуст на последней.
type ErrorPage struct {
	Items      []domain.ErrorItem
	NextCursor string
}

// ListErrors — объекты ОРД в error по (updated_at, id) для S-20 (до API администратора —
// команда maxim admin ord errors). Роль проверяет вызывающий.
func (s *Service) ListErrors(ctx context.Context, cursor string, limit int) (ErrorPage, error) {
	return s.ListErrorsOfType(ctx, "", cursor, limit)
}

// ListErrorsOfType — список ошибок одного вида (пусто — всех); API администратора, R-17.
func (s *Service) ListErrorsOfType(ctx context.Context, typ domain.ObjectType, cursor string, limit int) (ErrorPage, error) {
	const op = "Marking.ListErrors"
	if typ != "" && !typ.Valid() {
		return ErrorPage{}, apperr.Validation("type", "invalid")
	}
	if limit <= 0 {
		limit = errorsDefaultLimit
	}
	limit = min(limit, errorsMaxLimit)
	var after *domain.ErrorCursor
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var c domain.ErrorCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.ID == uuid.Nil {
			return ErrorPage{}, apperr.Validation("cursor", "invalid_cursor")
		}
		after = &c
	}
	items, err := s.Overview.ListErrors(ctx, typ, after, limit+1)
	if err != nil {
		return ErrorPage{}, err
	}
	page := ErrorPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		raw, _ := json.Marshal(domain.ErrorCursor{UpdatedAt: last.UpdatedAt, ID: last.Ref.ID, Type: last.Ref.Type})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	s.log.DebugContext(ctx, "ord errors listed", logx.KeyOp, op, "count", len(page.Items), "has_next", page.NextCursor != "")
	return page, nil
}

// Retry — «Повторить» администратора (S-20): объект из error снова pending и задание отправки.
// Креатив с erid повторно не отправляется (D-03, R-8): возобновляется только опрос статуса.
// Основание обязательно; действие пишется в audit_log с актором admin.
func (s *Service) Retry(ctx context.Context, ref domain.ObjectRef, adminID uuid.UUID, reason string) error {
	const op = "Marking.Retry"
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return apperr.Validation("reason", "required")
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &adminID})
	resumed := false
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		r, err := s.lock(ctx, ref)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("ord object not found")
		}
		if err != nil {
			return err
		}
		st, now := r.state(), s.Clock.Now()
		if st.SubmissionUncertain {
			return apperr.Conflict("ORD submission requires reconciliation")
		}
		if st.Status != domain.SyncError {
			return apperr.Conflict("ord object is not in error").WithDetail("sync_status", string(st.Status))
		}
		c, creative := r.(*creativeRecord)
		if (creative && c.v.HasErid()) || (isReportWrite(ref.Type) && st.RequestID != "" && domain.ClassifyOrdStatus(st.OrdStatus, true) != domain.ProgressFinalError) {
			// Опрос заново с полным сроком ожидания (R-10).
			st.Status, st.Error, st.SentAt = domain.SyncSent, "", now
			resumed = true
			if err := r.save(ctx, s, now); err != nil {
				return err
			}
			if err := s.enqueuePoll(ctx, ref, now); err != nil {
				return err
			}
		} else {
			if err := st.Retry(); err != nil {
				return err
			}
			if err := r.save(ctx, s, now); err != nil {
				return err
			}
			if err := s.enqueueSync(ctx, op, ref, reasonRetry); err != nil {
				return err
			}
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionOrdRetry, ObjectType: "ord_" + string(ref.Type), ObjectID: ref.ID.String(),
			ActorType: audit.ActorAdmin, Reason: &reason, Data: map[string]any{"resume_poll": resumed},
		})
	})
	if err != nil {
		s.log.WarnContext(ctx, "ord object retry rejected", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
			logx.KeyError, err.Error())
		return err
	}
	s.log.WarnContext(ctx, "ord object retry requested", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
		"admin_id", adminID, "resume_poll", resumed)
	return nil
}
