package app

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/stuck"
)

// DepositReport — состояние депозита Рокет Ворк (R-12, 06 §2.4).
type DepositReport struct {
	// API — остаток по данным сервиса.
	API money.Kopecks
	// Ledger — остаток счёта rocketwork_deposit в журнале.
	Ledger money.Kopecks
	// Payable, InFlight — обязательства: начислено и цена размещений в published, removed.
	Payable, InFlight money.Kopecks
	Need              money.Kopecks
	Tolerance         money.Kopecks
}

// Shortage — сколько не хватает до обязательств; 0 — хватает.
func (r DepositReport) Shortage() money.Kopecks { return max(r.Need-r.API, 0) }

// Mismatch — журнал и сервис расходятся больше допуска.
func (r DepositReport) Mismatch() bool { return domain.DepositMismatch(r.Ledger, r.API, r.Tolerance) }

// DepositReport собирает числа без оповещений (maxim admin money reconcile).
func (s *Service) DepositReport(ctx context.Context) (DepositReport, error) {
	api, err := s.Provider.GetDepositBalance(ctx)
	if err != nil {
		return DepositReport{}, err
	}
	ledger, err := s.Ledger.DepositBalance(ctx)
	if err != nil {
		return DepositReport{}, err
	}
	payable, err := s.Store.RocketworkPayable(ctx)
	if err != nil {
		return DepositReport{}, err
	}
	inFlight, err := s.Store.RocketworkInFlight(ctx)
	if err != nil {
		return DepositReport{}, err
	}
	return DepositReport{API: api, Ledger: ledger, Payable: payable, InFlight: inFlight,
		Need: domain.DepositNeed(payable, inFlight), Tolerance: s.Config.DepositTolerance}, nil
}

// CheckDeposit — задание check_rocketwork_deposit (R-12): нехватка и расхождение поднимают
// оповещения, исправленные — закрывают.
func (s *Service) CheckDeposit(ctx context.Context) (DepositReport, error) {
	const op = "Payout.CheckDeposit"
	r, err := s.DepositReport(ctx)
	if err != nil {
		return DepositReport{}, s.providerWait(ctx, op, err)
	}
	s.Metrics.Deposit("api", r.API)
	s.Metrics.Deposit("ledger", r.Ledger)
	s.log.InfoContext(ctx, "rocketwork deposit checked", logx.KeyOp, op, "api", int64(r.API), "ledger", int64(r.Ledger),
		"need", int64(r.Need), "payable", int64(r.Payable), "in_flight", int64(r.InFlight))
	if short := r.Shortage(); short > 0 {
		s.log.ErrorContext(ctx, "rocketwork deposit low", logx.KeyOp, op, "deposit", int64(r.API), "required", int64(r.Need),
			"shortage", int64(short))
		s.raise(ctx, alert.LevelError, AlertDepositLow, "", "top up the rocketwork deposit",
			map[string]any{"deposit": int64(r.API), "required": int64(r.Need), "shortage": int64(short)})
	} else {
		s.resolveLogged(ctx, AlertDepositLow)
	}
	if r.Mismatch() {
		s.log.ErrorContext(ctx, "rocketwork deposit mismatch", logx.KeyOp, op, "api", int64(r.API), "ledger", int64(r.Ledger),
			"tolerance", int64(r.Tolerance))
		s.raise(ctx, alert.LevelError, AlertDepositMismatch, "", "rocketwork deposit differs from the ledger",
			map[string]any{"api": int64(r.API), "ledger": int64(r.Ledger)})
	} else {
		s.resolveLogged(ctx, AlertDepositMismatch)
	}
	return r, nil
}

func (s *Service) resolveLogged(ctx context.Context, reason string) {
	s.resolve(ctx, reason, "")
	s.log.DebugContext(ctx, "rocketwork deposit alert resolved", logx.KeyOp, "Payout.CheckDeposit", "reason", reason)
}

// Проверки зависших (R-14, 07 §3).
const (
	StuckAcceptedUnpaid = "payout_accepted_unpaid"
	StuckSentNoOutcome  = "payout_sent_no_outcome"
	StuckCreatedNotSent = "payout_created_not_sent"
)

// StuckChecks — проверки для platform/stuck.
func (s *Service) StuckChecks() []stuck.Check {
	keys := func(f func(ctx context.Context, before time.Time) ([]domain.StuckItem, error)) func(context.Context, time.Time) ([]string, error) {
		return func(ctx context.Context, before time.Time) ([]string, error) {
			items, err := f(ctx, before)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(items))
			for _, it := range items {
				out = append(out, it.ObjectKey)
			}
			return out, nil
		}
	}
	return []stuck.Check{
		{Name: StuckAcceptedUnpaid, Threshold: s.Config.StuckAcceptedAge, Level: alert.LevelError, Find: keys(s.Store.AcceptedUnpaid)},
		{Name: StuckSentNoOutcome, Threshold: s.Config.StuckSentAge, Level: alert.LevelError,
			Find: keys(func(ctx context.Context, before time.Time) ([]domain.StuckItem, error) {
				return s.Store.PayoutsInStatusBefore(ctx, domain.StatusSent, "", before)
			})},
		{Name: StuckCreatedNotSent, Threshold: s.Config.StuckCreatedAge, Level: alert.LevelWarn,
			Find: keys(func(ctx context.Context, before time.Time) ([]domain.StuckItem, error) {
				return s.Store.PayoutsInStatusBefore(ctx, domain.StatusCreated, domain.MethodRocketwork, before)
			})},
	}
}
