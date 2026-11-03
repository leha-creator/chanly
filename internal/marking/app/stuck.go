package app

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// stuckLimit — сколько зависших объектов одного вида берёт одна проверка.
const stuckLimit = 500

// Проверки зависших объектов (07 §3) для platform/stuck; проверки собирает bootstrap.
const (
	StuckEridOverdue = alertEridOverdue
	StuckSentOverdue = alertSentOverdue
)

// OverdueErid — версии в approved без erid дольше порога (T-11): креатив создан при одобрении
// версии и всё ещё без токена. Ключ — идентификатор версии.
func (s *Service) OverdueErid(ctx context.Context, olderThan time.Time) ([]string, error) {
	list, err := s.Overview.CreativesWithoutErid(ctx, olderThan, stuckLimit)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(list))
	for _, c := range list {
		keys = append(keys, c.PostVersionID.String())
		s.log.WarnContext(ctx, "post version erid overdue", logx.KeyOp, "Marking.OverdueErid", KeyVersionID, c.PostVersionID,
			KeyCreativeID, c.ID, "since", c.CreatedAt, "sync_status", c.Sync.Status)
	}
	return keys, nil
}

// OverdueSent — объекты ОРД в sent дольше порога. Ключ — <type>:<id>.
func (s *Service) OverdueSent(ctx context.Context, olderThan time.Time) ([]string, error) {
	list, err := s.Overview.StuckSent(ctx, olderThan, stuckLimit)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(list))
	for _, o := range list {
		keys = append(keys, o.Ref.String())
		s.log.WarnContext(ctx, "ord object sent overdue", logx.KeyOp, "Marking.OverdueSent", KeyObjectType, o.Ref.Type,
			KeyObjectID, o.Ref.ID, "since", o.SentAt)
	}
	return keys, nil
}
