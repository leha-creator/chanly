package app

import (
	"context"
	"fmt"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Counters — счётчики меню администратора (R-16).
type Counters struct {
	ChannelsModeration        int
	OrdersModeration          int
	DisputesOpen              int
	DisputesOverdue           int
	OrdErrors                 int
	BankTransactionsUnmatched int
	PayoutsFailed             int
	ApprovalsPending          int
	OrdReportPending          bool
}

// Counter дополняет счётчики своего модуля (переходники bootstrap/bridge над модулями).
type Counter interface {
	Name() string
	Count(ctx context.Context, c *Counters) error
}

// Counters собирает счётчики всех модулей; подтверждения считает сам модуль.
func (s *Service) Counters(ctx context.Context) (Counters, error) {
	var c Counters
	for _, m := range s.Deps.Counters {
		if err := m.Count(ctx, &c); err != nil {
			s.log.ErrorContext(ctx, "admin counter failed", logx.KeyOp, "Admin.Counters", "counter", m.Name(),
				logx.KeyError, err.Error())
			return Counters{}, fmt.Errorf("admin counters %s: %w", m.Name(), err)
		}
	}
	n, err := s.PendingCount(ctx)
	if err != nil {
		return Counters{}, err
	}
	c.ApprovalsPending = n
	return c, nil
}
