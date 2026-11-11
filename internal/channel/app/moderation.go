package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Операции администратора для каналов (R-1, S-16, 06 §6.3). Роль проверяет httpapi;
// здесь — только выборки ...ForAdmin и решения в транзакции с журналом действий.
// Текст причины отказа и комментарий проверки пишутся только в audit_log (07 §5.4).

// AdminListRequest — фильтры и страница очереди администратора.
type AdminListRequest struct {
	Status        *domain.Status
	RknStatus     *domain.RknStatus
	RknRecheckDue *bool
	Cursor        string
	Limit         int
}

// AdminPage — страница очереди; NextCursor пуст на последней.
type AdminPage struct {
	Items      []domain.AdminChannel
	NextCursor string
}

// AdminChannelView — канал для экрана модерации (S-16 шаг 1).
type AdminChannelView struct {
	domain.AdminChannel
	RknRecheckDue  bool
	History        []domain.MetricsSnapshot
	Moderation     []domain.ModerationEntry
	Unavailability []domain.Unavailability
}

// RknRecheckDue — номер подтверждён давно, пора проверить снова (R-17).
func (s *Service) RknRecheckDue(c *domain.Channel) bool {
	return c.RknRecheckDue(s.Clock.Now(), s.Config.RknRecheckInterval)
}

// AdminList — каналы для администратора, старые изменения первыми.
func (s *Service) AdminList(ctx context.Context, r AdminListRequest) (AdminPage, error) {
	const op = "Channel.AdminList"
	if r.Limit <= 0 {
		r.Limit = catalogDefaultLimit
	}
	r.Limit = min(r.Limit, catalogMaxLimit)
	f := domain.AdminFilter{
		Status: r.Status, RknStatus: r.RknStatus, RknRecheckDue: r.RknRecheckDue,
		RecheckBefore: s.Clock.Now().Add(-s.Config.RknRecheckInterval), Limit: r.Limit + 1,
	}
	if r.Cursor != "" {
		c, err := decodeAdminCursor(r.Cursor)
		if err != nil {
			s.log.WarnContext(ctx, "admin channels query rejected", logx.KeyOp, op, "reason", "invalid_cursor")
			return AdminPage{}, apperr.Validation("cursor", "invalid_cursor")
		}
		f.After = c
	}
	items, err := s.Admin.ListForAdmin(ctx, f)
	if err != nil {
		return AdminPage{}, err
	}
	page := AdminPage{Items: items}
	if len(items) > r.Limit {
		page.Items = items[:r.Limit]
		last := page.Items[r.Limit-1].Channel
		raw, _ := json.Marshal(domain.AdminCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	s.log.DebugContext(ctx, "admin channels listed", logx.KeyOp, op, "count", len(page.Items), "has_next", page.NextCursor != "")
	return page, nil
}

func decodeAdminCursor(s string) (*domain.AdminCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c domain.AdminCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == uuid.Nil {
		return nil, errors.New("invalid admin cursor")
	}
	return &c, nil
}

// AdminGet — канал со сведениями MAX, метриками за 30 суток и историей модерации.
func (s *Service) AdminGet(ctx context.Context, id uuid.UUID) (*AdminChannelView, error) {
	ch, err := s.Admin.GetForAdmin(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("channel not found")
	}
	if err != nil {
		return nil, err
	}
	v := &AdminChannelView{AdminChannel: *ch, RknRecheckDue: s.RknRecheckDue(ch.Channel)}
	if v.History, err = s.Metrics.Recent(ctx, id, catalogHistoryDays); err != nil {
		return nil, err
	}
	if v.Moderation, err = s.Admin.ModerationHistoryForAdmin(ctx, id); err != nil {
		return nil, err
	}
	if v.Unavailability, err = s.Unavail.ListLive(ctx, id, s.Clock.Now()); err != nil {
		return nil, err
	}
	return v, nil
}

// Approve — одобрение канала (S-16 шаг 3, R-18).
func (s *Service) Approve(ctx context.Context, adminID, id uuid.UUID) (*AdminChannelView, error) {
	const op = "Channel.Approve"
	err := s.adminDecision(ctx, op, id, func(c *domain.Channel) (domain.StatusChange, audit.Entry, error) {
		change, err := c.Approve(adminID, s.Config.RknThreshold, s.Clock.Now())
		var blocked *domain.ApproveBlockedError
		if errors.As(err, &blocked) {
			s.log.WarnContext(ctx, "channel approve blocked", logx.KeyOp, op, KeyChannelID, c.ID, "missing", blocked.Missing)
			return change, audit.Entry{}, apperr.Conflict("channel does not meet approval requirements").WithDetail("missing", blocked.Missing)
		}
		return change, audit.Entry{Action: "channel.approved", Data: map[string]any{"status": c.Status}}, err
	}, func(ctx context.Context, c *domain.Channel) error {
		// R-13: ошибка ОРД одобрение не отменяет — ОРД вызывается позже заданием; здесь только записи.
		return s.channelApproved(ctx, op, c)
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "channel approved", logx.KeyOp, op, KeyChannelID, id, "admin_id", adminID)
	return s.AdminGet(ctx, id)
}

// Reject — отказ модерации с причиной (S-16).
func (s *Service) Reject(ctx context.Context, adminID, id uuid.UUID, reason string) (*AdminChannelView, error) {
	const op = "Channel.Reject"
	err := s.adminDecision(ctx, op, id, func(c *domain.Channel) (domain.StatusChange, audit.Entry, error) {
		change, err := c.Reject(reason, s.Clock.Now())
		text := c.StatusReason
		return change, audit.Entry{Action: "channel.rejected", Reason: &text, Data: map[string]any{"status": c.Status}}, err
	}, nil)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "channel rejected", logx.KeyOp, op, KeyChannelID, id, "admin_id", adminID)
	return s.AdminGet(ctx, id)
}

// SetRknCheck — итог ручной проверки номера РКН (06 §6.3, Q-34, R-18), затем правило R-16.
func (s *Service) SetRknCheck(ctx context.Context, adminID, id uuid.UUID, result domain.RknCheckResult, comment string) (*AdminChannelView, error) {
	const op = "Channel.SetRknCheck"
	err := s.adminDecision(ctx, op, id, func(c *domain.Channel) (domain.StatusChange, audit.Entry, error) {
		change, err := c.RknChecked(result, adminID, s.Config.RknThreshold, s.Clock.Now())
		e := audit.Entry{Action: "channel.rkn_checked", Data: map[string]any{"result": string(result), "rkn_status": c.RknStatus}}
		if comment != "" {
			e.Reason = &comment
		}
		return change, e, err
	}, nil)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "channel rkn checked", logx.KeyOp, op, KeyChannelID, id, "result", result, "admin_id", adminID)
	return s.AdminGet(ctx, id)
}

// adminDecision — общий ход решения администратора: канал под блокировкой, правило
// domain, сохранение со сменой доступности (R-11) и запись журнала действий с актором admin.
// after — последствия решения в той же транзакции после сохранения; nil — нет.
func (s *Service) adminDecision(ctx context.Context, op string, id uuid.UUID,
	decide func(c *domain.Channel) (domain.StatusChange, audit.Entry, error),
	after func(ctx context.Context, c *domain.Channel) error) error {
	ctx = audit.WithActor(ctx, adminActor(ctx))
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Admin.GetForAdminForUpdate(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		change, entry, err := decide(c)
		switch {
		case errors.Is(err, domain.ErrNotInModeration):
			s.log.WarnContext(ctx, "admin channel decision rejected", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status)
			return apperr.Conflict("channel is not in moderation").WithDetail("status", string(c.Status))
		case errors.Is(err, domain.ErrRknNotCheckable):
			s.log.WarnContext(ctx, "admin channel decision rejected", logx.KeyOp, op, KeyChannelID, c.ID, "status", c.Status,
				"has_rkn_number", c.RknNumber != "")
			return apperr.Conflict("rkn number cannot be checked").WithDetail("status", string(c.Status))
		case err != nil:
			return ruleError(err)
		}
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		entry.ObjectType, entry.ObjectID, entry.ActorType, entry.OrganizationID = "channel", c.ID.String(), audit.ActorAdmin, c.OrganizationID
		if err := s.Audit.Record(ctx, entry); err != nil {
			return err
		}
		if after == nil {
			return nil
		}
		return after(ctx, c)
	})
}

// adminActor — действующее лицо решения: пользователь сессии с типом admin.
func adminActor(ctx context.Context) audit.Actor {
	a := audit.ActorFrom(ctx)
	a.Type = audit.ActorAdmin
	return a
}
