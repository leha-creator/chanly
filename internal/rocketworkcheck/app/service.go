package app

import (
	"context"
	"errors"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"log/slog"

	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

var ErrReconcile = errors.New("diagnostic action already attempted; reconcile required")

type Service struct {
	Store    domain.ControlStore
	Log      *slog.Logger
	Tx       tx.Runner
	Ledger   domain.Ledger
	ArmingID string
	Provider domain.Provider
}

// Intent is committed by the store before returning network permission. No
// external call belongs in that transaction. Repeat calls never authorize POST.
func (s *Service) Intent(ctx context.Context, id string, action domain.Action, hash string, e domain.Executor) error {
	s.Log.DebugContext(ctx, "diagnostic authorization started", "run", id, "action", action)
	r, err := s.Store.Run(ctx, id)
	if err != nil {
		return domain.ErrDenied
	}
	g, err := s.Store.Grant(ctx, r.GrantID)
	if err != nil {
		return domain.ErrDenied
	}
	if s.ArmingID == "" || s.ArmingID != g.Policy.ArmingID {
		return domain.ErrDenied
	}
	now, err := s.Store.Now(ctx)
	if err != nil {
		return domain.ErrDenied
	}
	if err = g.Policy.Authorize(now, r.AccountID, e, r.Nominal, action); err != nil {
		s.Log.WarnContext(ctx, "diagnostic policy denied", "run", id, "action", action)
		return err
	}
	ok, err := s.Store.Intent(ctx, id, action, hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrReconcile
	}
	s.Log.InfoContext(ctx, "diagnostic action authorized", "run", id, "action", action, "nominal", r.Nominal)
	return nil
}
