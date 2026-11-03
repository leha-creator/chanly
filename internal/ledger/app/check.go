package app

import (
	"context"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// KindCheckBalance — периодическое задание проверки сходимости (R-15).
const KindCheckBalance = "check_ledger_balance"

// Причины оповещений проверки сходимости (R-15, 07 §3).
const (
	ReasonImbalance         = "ledger_imbalance"
	ReasonCacheMismatch     = "ledger_cache_mismatch"
	ReasonHoldMismatch      = "ledger_hold_mismatch"
	ReasonNegativeLiability = "ledger_negative_liability"
)

// Проверки для метрики maxim_ledger_check_failures_total{check}.
const (
	CheckSides     = "sides"
	CheckCache     = "cache"
	CheckHolds     = "holds"
	CheckNegatives = "negative"
)

// CheckReport — итог проверки сходимости.
type CheckReport struct {
	Assets          money.Kopecks
	Liabilities     money.Kopecks
	CacheMismatches []domain.CacheMismatch
	HoldMismatches  []domain.HoldMismatch
	Negatives       []domain.NegativeAccount
}

// OK — расхождений нет.
func (r CheckReport) OK() bool {
	return r.Assets == r.Liabilities && len(r.CacheMismatches) == 0 && len(r.HoldMismatches) == 0 && len(r.Negatives) == 0
}

// OpenKeysLister — объекты открытых оповещений причины: закрываются те, что восстановились.
type OpenKeysLister interface {
	OpenKeys(ctx context.Context, reason string) ([]string, error)
}

// CheckBalance — сходимость журнала (R-15, 06 §2.2): (1) активы = пассивы, (2) кеш остатка
// = сумма проводок по каждому счёту, (3) остаток placement_hold = активное удержание,
// (4) пассивы не отрицательны. Расхождение — ERROR, метрика и оповещение; восстановленное —
// закрытие оповещения. Ошибка возвращается только при сбое чтения.
func (s *Service) CheckBalance(ctx context.Context) (CheckReport, error) {
	const op = "Ledger.CheckBalance"
	start := s.Clock.Now()
	var r CheckReport
	var err error
	if r.Assets, r.Liabilities, err = s.Reports.SidesTotal(ctx); err != nil {
		return r, err
	}
	if r.CacheMismatches, err = s.Reports.CacheMismatches(ctx); err != nil {
		return r, err
	}
	if r.HoldMismatches, err = s.Reports.HoldMismatches(ctx); err != nil {
		return r, err
	}
	if r.Negatives, err = s.Reports.NegativeLiabilities(ctx); err != nil {
		return r, err
	}

	if r.Assets != r.Liabilities {
		s.Metrics.CheckFailed(CheckSides)
		s.log.ErrorContext(ctx, "ledger imbalance", logx.KeyOp, op, "assets", int64(r.Assets),
			"liabilities", int64(r.Liabilities), "diff", int64(r.Assets-r.Liabilities))
		s.raise(ctx, op, alert.Alert{Reason: ReasonImbalance, Level: alert.LevelError,
			Summary: "Сумма активов журнала не равна сумме пассивов",
			Fields:  map[string]any{"assets": int64(r.Assets), "liabilities": int64(r.Liabilities)}})
	} else {
		s.resolve(ctx, op, ReasonImbalance, "")
	}

	cacheKeys := make(map[string]bool, len(r.CacheMismatches))
	for _, m := range r.CacheMismatches {
		s.Metrics.CheckFailed(CheckCache)
		s.log.ErrorContext(ctx, "ledger cache mismatch", logx.KeyOp, op, "account_id", m.AccountID, "kind", string(m.Kind),
			"cached", int64(m.Cached), "actual", int64(m.Computed))
		key := m.AccountID.String()
		cacheKeys[key] = true
		s.raise(ctx, op, alert.Alert{Reason: ReasonCacheMismatch, ObjectKey: key, Level: alert.LevelError,
			Summary: "Кеш остатка счёта журнала не равен сумме проводок",
			Fields:  map[string]any{"account_id": key, "kind": string(m.Kind), "cached": int64(m.Cached), "actual": int64(m.Computed)}})
	}
	s.resolveRecovered(ctx, op, ReasonCacheMismatch, cacheKeys)

	holdKeys := make(map[string]bool, len(r.HoldMismatches))
	for _, m := range r.HoldMismatches {
		s.Metrics.CheckFailed(CheckHolds)
		status := "none"
		if m.Held > 0 {
			status = "active"
		}
		s.log.ErrorContext(ctx, "ledger hold mismatch", logx.KeyOp, op, KeyPlacementID, m.PlacementID, "hold_status", status,
			"balance", int64(m.Balance), "held", int64(m.Held))
		key := m.PlacementID.String()
		holdKeys[key] = true
		s.raise(ctx, op, alert.Alert{Reason: ReasonHoldMismatch, ObjectKey: key, Level: alert.LevelError,
			Summary: "Остаток удержания размещения не равен сумме удержания",
			Fields:  map[string]any{KeyPlacementID: key, "hold_status": status, "balance": int64(m.Balance), "held": int64(m.Held)}})
	}
	s.resolveRecovered(ctx, op, ReasonHoldMismatch, holdKeys)

	negKeys := make(map[string]bool, len(r.Negatives))
	for _, n := range r.Negatives {
		s.Metrics.CheckFailed(CheckNegatives)
		s.log.ErrorContext(ctx, "ledger negative liability", logx.KeyOp, op, "account_id", n.AccountID, "kind", string(n.Kind),
			"balance", int64(n.Balance))
		key := n.AccountID.String()
		negKeys[key] = true
		s.raise(ctx, op, alert.Alert{Reason: ReasonNegativeLiability, ObjectKey: key, Level: alert.LevelError,
			Summary: "Отрицательный остаток пассива журнала",
			Fields:  map[string]any{"account_id": key, "kind": string(n.Kind), "balance": int64(n.Balance)}})
	}
	s.resolveRecovered(ctx, op, ReasonNegativeLiability, negKeys)

	if r.OK() {
		s.log.InfoContext(ctx, "ledger balance check passed", logx.KeyOp, op, "assets", int64(r.Assets),
			"liabilities", int64(r.Liabilities), "elapsed_ms", s.Clock.Now().Sub(start).Milliseconds())
	}
	return r, nil
}

// resolveRecovered закрывает открытые оповещения причины по объектам, которых больше нет
// среди расхождений.
func (s *Service) resolveRecovered(ctx context.Context, op, reason string, current map[string]bool) {
	lister, ok := s.Alerts.(OpenKeysLister)
	if !ok {
		return
	}
	open, err := lister.OpenKeys(ctx, reason)
	if err != nil {
		s.log.ErrorContext(ctx, "open alerts query failed", logx.KeyOp, op, "reason", reason, logx.KeyError, err.Error())
		return
	}
	for _, key := range open {
		if !current[key] {
			s.resolve(ctx, op, reason, key)
		}
	}
}
