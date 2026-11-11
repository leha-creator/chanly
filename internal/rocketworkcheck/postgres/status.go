package postgres

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

func (s *Store) Status(ctx context.Context, id string) (map[string]any, error) {
	r, err := s.Run(ctx, id)
	if err != nil {
		return nil, domain.ErrDenied
	}
	g, err := s.Grant(ctx, r.GrantID)
	if err != nil {
		return nil, domain.ErrDenied
	}
	b, err := s.q(ctx).DiagnosticBudgetStatus(ctx, r.AccountID)
	if err != nil {
		return nil, domain.ErrDenied
	}
	next := "reconcile"
	var m domain.Manifest
	if json.Unmarshal(r.Manifest, &m) == nil {
		for _, action := range m.Actions {
			c, err := s.Checkpoint(ctx, r.ID, action)
			if err != nil {
				next = string(action)
				break
			}
			if c.State == "intent" || c.State == "uncertain" {
				next = "reconcile"
				break
			}
		}
	}
	if r.State == "settled" || r.State == "released" {
		next = "done"
	}
	now, err := s.Now(ctx)
	if err != nil {
		return nil, domain.ErrDenied
	}
	if next != "done" && (b.Killed || g.Policy.Revoked || !now.Before(g.Policy.ExpiresAt) || now.Before(g.Policy.NotBefore)) {
		next = "reconcile_only"
	}
	return map[string]any{
		"run_id": r.ID, "grant_id": r.GrantID, "state": r.State, "policy_version": g.Policy.Version,
		"nominal_kopecks": r.Nominal, "max_commission_kopecks": r.FeeCeiling,
		"actual_nominal_kopecks": r.ActualNominal, "actual_fee_kopecks": r.ActualFee,
		"reserved_at": r.ReservedAt, "settled_at": r.SettledAt,
		"provider_paid_at": r.ProviderPaidAt,
		"grant_expires_at": g.Policy.ExpiresAt, "grant_revoked": g.Policy.Revoked, "killed": b.Killed,
		"account_total_limit_kopecks": b.TotalLimit, "account_daily_limit_kopecks": b.DailyLimit,
		"grant_total_limit_kopecks": g.Policy.TotalBudget, "grant_daily_limit_kopecks": g.Policy.DailyBudget,
		"spent_kopecks": b.Spent, "daily_spent_kopecks": b.DailySpent, "held_kopecks": b.Held,
		"remaining_kopecks":       effectiveRemaining(b.TotalLimit, int64(g.Policy.TotalBudget), b.Spent, b.Held),
		"daily_remaining_kopecks": effectiveRemaining(b.DailyLimit, int64(g.Policy.DailyBudget), b.DailySpent, b.Held),
		"daily_operations":        b.Operations, "daily_operations_limit": min(b.DailyOperations, g.Policy.DailyOperations),
		"next_step": next, "network_used": false,
	}, nil
}

// Numeric SQL sums can exceed int64 when the provider exceeds agreed fees.
// Preserve the full negative remainder instead of overflowing a money counter.
func effectiveRemaining(account string, grant int64, spent, held string) string {
	limit, ok := new(big.Int).SetString(account, 10)
	if !ok {
		return "unknown"
	}
	if limit.Cmp(big.NewInt(grant)) > 0 {
		limit.SetInt64(grant)
	}
	for _, value := range []string{spent, held} {
		n, ok := new(big.Int).SetString(value, 10)
		if !ok {
			return "unknown"
		}
		limit.Sub(limit, n)
	}
	return limit.String()
}
