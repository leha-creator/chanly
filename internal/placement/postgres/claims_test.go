package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestClaimsAndResolution(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	p := f.placement(f.order(t0), f.channel, domain.StatusRemoved, t0)
	c, err := domain.NewClaim(ids.New(), p.ID, f.user, domain.ClaimTopViolated, "Чужой пост в первый час", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Claims().Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	c2 := c
	c2.ID = ids.New()
	if err := f.s.Claims().Insert(ctx, c2); !errors.Is(err, domain.ErrClaimExists) {
		t.Fatalf("second claim: %v", err)
	}
	got, err := f.s.Claims().GetByPlacement(ctx, p.ID)
	if err != nil || got.ID != c.ID || got.Text != c.Text || got.Reason != domain.ClaimTopViolated {
		t.Fatalf("claim %+v %v", got, err)
	}
	if m, err := f.s.Claims().ListByPlacements(ctx, []uuid.UUID{p.ID, ids.New()}); err != nil || len(m) != 1 {
		t.Fatalf("list claims %v %v", m, err)
	}

	share := 5000
	d := domain.NewClaimDispute(ids.New(), c, &share, t0, domain.DefaultControlRules)
	if err := f.s.Disputes().Insert(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Второй спор открыт системой, срок раньше — первым в очереди просроченных.
	p2 := f.placement(f.order(t0), f.newChannel(f.blogger), domain.StatusPublished, t0)
	d2 := domain.NewAdminDispute(ids.New(), p2.ID, t0.Add(-time.Hour), domain.DefaultControlRules)
	if err := f.s.Disputes().Insert(ctx, d2); err != nil {
		t.Fatal(err)
	}
	overdue := t0.Add(72 * time.Hour)
	open, err := f.s.Disputes().ListAdmin(ctx, domain.DisputeFilter{OverdueAt: &overdue})
	if err != nil || len(open) != 2 || open[0].ID != d2.ID {
		t.Fatalf("overdue %+v %v", open, err)
	}

	locked, err := f.s.Disputes().GetByPlacementForUpdate(ctx, p.ID)
	if err != nil || *locked.ClaimID != c.ID {
		t.Fatalf("locked %+v %v", locked, err)
	}
	if err := locked.Resolve(domain.ResolutionProportional, 5000, "топ нарушен", f.user, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Disputes().Resolve(ctx, locked); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Disputes().Resolve(ctx, locked); !errors.Is(err, domain.ErrDisputeResolved) {
		t.Fatalf("second resolve: %v", err)
	}
	resolved, _ := f.s.Disputes().GetByPlacement(ctx, p.ID)
	if resolved.Resolution != domain.ResolutionProportional || *resolved.PerformanceShare != 5000 ||
		resolved.ResolutionComment != "топ нарушен" || *resolved.ResolvedBy != f.user {
		t.Fatalf("resolved %+v", resolved)
	}
	open, _ = f.s.Disputes().ListAdmin(ctx, domain.DisputeFilter{})
	all, _ := f.s.Disputes().ListAdmin(ctx, domain.DisputeFilter{All: true})
	if len(open) != 1 || len(all) != 2 {
		t.Fatalf("open %d all %d", len(open), len(all))
	}
}

func TestDisputeConstraints(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	p := f.placement(f.order(t0), f.channel, domain.StatusDisputed, t0)
	d := domain.NewAdminDispute(ids.New(), p.ID, t0, domain.DefaultControlRules)
	if err := f.s.Disputes().Insert(ctx, d); err != nil {
		t.Fatal(err)
	}
	pool := f.d.Pool
	upd := `UPDATE dispute SET resolution = $2, performance_share = $3, resolution_comment = 'x', resolved_by = $4,
		resolved_at = now() WHERE id = $1`
	for name, args := range map[string][]any{
		"proportional 10000":  {"proportional", 10000, f.user},
		"proportional 0":      {"proportional", 0, f.user},
		"full payout 5000":    {"full_payout", 5000, f.user},
		"full refund 1":       {"full_refund", 1, f.user},
		"without resolved_by": {"full_refund", 0, nil},
	} {
		if _, err := pool.Exec(ctx, upd, append([]any{d.ID}, args...)...); err == nil {
			t.Errorf("%s: updated", name)
		}
	}
	// Основание спора — существующая претензия.
	if _, err := pool.Exec(ctx, `UPDATE dispute SET claim_id = $2 WHERE id = $1`, d.ID, ids.New()); err == nil ||
		!strings.Contains(err.Error(), "dispute_claim_id_fkey") {
		t.Errorf("unknown claim_id: %v", err)
	}
	c, _ := domain.NewClaim(ids.New(), p.ID, f.user, domain.ClaimOther, "x", t0)
	if err := f.s.Claims().Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO claim (id, placement_id, reason, text, created_by, created_at)
		VALUES ($1, $2, 'other', $3, $4, now())`, ids.New(), f.placement(f.order(t0), f.newChannel(f.blogger),
		domain.StatusRemoved, t0).ID, strings.Repeat("a", 2001), f.user); err == nil {
		t.Error("claim text 2001 inserted")
	}
	// Роль приложения не меняет и не удаляет претензии, не удаляет споры.
	if _, err := pool.Exec(ctx, `UPDATE claim SET text = 'y'`); err == nil {
		t.Error("app role updated claim")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM claim`); err == nil {
		t.Error("app role deleted claim")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dispute`); err == nil {
		t.Error("app role deleted dispute")
	}
}
