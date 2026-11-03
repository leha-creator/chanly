package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

// acceptedForPayout — опубликованное размещение, переведённое в accepted строкой (приёмка — следующий этап).
func (e *env) acceptedForPayout() uuid.UUID {
	e.t.Helper()
	pid, _, _ := e.published()
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE placement SET status = 'accepted', performance_share = 10000,
		payout_amount = blogger_amount, fee_charged = fee_amount, refund_amount = 0, lock_version = lock_version + 1
		WHERE id = $1`, pid); err != nil {
		e.t.Fatal(err)
	}
	return pid
}

func TestMarkPaid(t *testing.T) {
	e := newEnv(t)
	pid := e.acceptedForPayout()
	payoutID := uuid.New()
	if err := e.tx.InTx(context.Background(), func(ctx context.Context) error {
		return e.svc.MarkPaid(ctx, pid, payoutID)
	}); err != nil {
		t.Fatal(err)
	}
	if p := e.placement(pid); p.Status != domain.StatusPaid || p.StatusReason != "payout_succeeded" {
		t.Fatalf("placement %s %s", p.Status, p.StatusReason)
	}
	if n := e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND transition = 'P-16'
		AND actor_type = 'system' AND idempotency_key = $2`, pid, "payout:"+payoutID.String()); n != 1 {
		t.Fatalf("history rows %d", n)
	}
	if len(e.logged("placement paid")) != 1 {
		t.Fatal("not logged")
	}
	// Повтор P-16 из paid отклоняется.
	err := e.tx.InTx(context.Background(), func(ctx context.Context) error { return e.svc.MarkPaid(ctx, pid, payoutID) })
	if !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatalf("second P-16: %v", err)
	}
}

func TestMarkPaidRejectsNotAccepted(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	err := e.tx.InTx(context.Background(), func(ctx context.Context) error { return e.svc.MarkPaid(ctx, pid, uuid.New()) })
	if !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatalf("P-16 from published: %v", err)
	}
	if p := e.placement(pid); p.Status != domain.StatusPublished {
		t.Fatalf("status %s", p.Status)
	}
}
