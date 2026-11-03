package app_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// claimed — размещение в споре по претензии рекламодателя из removed.
func (e *env) claimed(reason domain.ClaimReason) uuid.UUID {
	e.t.Helper()
	pid := e.removed()
	if _, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{Reason: reason, Text: "претензия"}); err != nil {
		e.t.Fatal(err)
	}
	return pid
}

func (e *env) rating(pid uuid.UUID) int {
	e.t.Helper()
	var r int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT rating FROM channel WHERE id = $1`,
		e.placement(pid).ChannelID).Scan(&r); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) resolve(pid uuid.UUID, res domain.Resolution, share *int) (app.ResolveResult, error) {
	return e.svc.ResolveDispute(context.Background(), app.ResolveInput{PlacementID: pid, Resolution: res, Share: share,
		Comment: "решение по материалам", AdminID: e.admin()})
}

func TestResolveFullPayout(t *testing.T) {
	e := newEnv(t)
	pid := e.claimed(domain.ClaimOther)
	rating := e.rating(pid)
	r, err := e.resolve(pid, domain.ResolutionFullPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := e.placement(pid)
	if p.Status != domain.StatusAccepted || *p.PerformanceShare != 10000 || r.PayoutID == uuid.Nil ||
		r.Distribution.PayoutAmount != p.BloggerAmount {
		t.Fatalf("full payout %+v %+v", p, r)
	}
	if by, _ := e.holds.SettledBy(pid); by.Actor != domain.ActorAdmin || *by.UserID != e.admin() {
		t.Fatalf("settled by %+v", by)
	}
	if e.rating(pid) != rating {
		t.Fatal("rating changed on full payout")
	}
	if e.notices.Count(domain.NoticeDisputeResolved) != 2 {
		t.Fatalf("notices %v", e.notices.Templates())
	}
	if !slices.Contains(e.alerts.resolved, "placement_disputed:"+pid.String()) {
		t.Fatalf("alerts resolved %v", e.alerts.resolved)
	}
	d := e.dispute(pid)
	if d.Resolution != domain.ResolutionFullPayout || *d.ResolvedBy != e.admin() {
		t.Fatalf("dispute %+v", d)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'dispute.resolved' AND object_id = $1`, pid.String()) != 1 {
		t.Fatal("audit")
	}
	// Решение окончательное.
	_, err = e.resolve(pid, domain.ResolutionFullRefund, nil)
	wantReason(t, err, apperr.CodeConflict, "dispute_resolved")
	// T-06 после решения оповещения не поднимает.
	e.clk.Set(d.DeadlineAt.Add(time.Minute))
	e.runJobs()
	if slices.Contains(e.alerts.reasons(), "dispute_overdue") {
		t.Fatal("dispute_overdue after resolution")
	}
}

func TestResolveProportional(t *testing.T) {
	e := newEnv(t)
	pid := e.claimed(domain.ClaimTopViolated)
	rating := e.rating(pid)
	// Без доли — предложение системы (top_violated — 5000).
	r, err := e.resolve(pid, domain.ResolutionProportional, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := e.placement(pid)
	want, _ := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, 5000)
	if p.Status != domain.StatusAccepted || r.Distribution != want || *p.RefundAmount != want.RefundAmount {
		t.Fatalf("proportional %+v want %+v", r.Distribution, want)
	}
	if got := e.payouts.Created(); len(got) != 1 || got[0].Amount != want.PayoutAmount {
		t.Fatalf("payouts %+v", got)
	}
	if e.rating(pid) != max(rating-5, 0) || r.RatingFrom != rating {
		t.Fatalf("rating %d → %d", rating, e.rating(pid))
	}
	n, ok := e.notices.Last(domain.NoticeDisputeResolved, domain.ActorAdvertiser)
	if !ok || n.Data["refund_amount"] != int64(want.RefundAmount) || n.Data["performance_share"] != 5000 {
		t.Fatalf("notice %+v", n)
	}
}

func TestResolveShareRequired(t *testing.T) {
	e := newEnv(t)
	pid := e.claimed(domain.ClaimContentMismatch)
	_, err := e.resolve(pid, domain.ResolutionProportional, nil)
	wantReason(t, err, apperr.CodeValidationFailed, "")
	if e.placement(pid).Status != domain.StatusDisputed || e.dispute(pid).Resolved() {
		t.Fatal("changed")
	}
	// Явная доля — пример 03 §7.
	share := 7500
	r, err := e.resolve(pid, domain.ResolutionProportional, &share)
	if err != nil || r.Distribution.PerformanceShare != 7500 {
		t.Fatalf("explicit share: %v %+v", err, r)
	}
}

func TestResolveFullRefundRemovesLivePost(t *testing.T) {
	e := newEnv(t)
	pid, _, msgID := e.published()
	if _, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{Reason: domain.ClaimContentMismatch,
		Text: "не тот пост"}); err != nil {
		t.Fatal(err)
	}
	rating := e.rating(pid)
	// accepted при живой публикации — отказ.
	_, err := e.resolve(pid, domain.ResolutionFullPayout, nil)
	wantReason(t, err, apperr.CodeInvalidTransition, domain.ReasonPublicationLive)
	if len(e.logged("placement invariant violated")) != 0 {
		t.Fatal("expected refusal logged as invariant violation")
	}

	r, err := e.resolve(pid, domain.ResolutionFullRefund, nil)
	if err != nil || !r.RemovalScheduled || r.PayoutID != uuid.Nil {
		t.Fatalf("refund: %v %+v", err, r)
	}
	p := e.placement(pid)
	if p.Status != domain.StatusRefunded || *p.RefundAmount != p.TotalAmount || e.holds.Active(pid) {
		t.Fatalf("refunded %+v", p)
	}
	if e.rating(pid) != max(rating-10, 0) {
		t.Fatalf("rating %d → %d", rating, e.rating(pid))
	}
	e.runJobs()
	pub := e.publication(pid)
	if pub.Live() || pub.RemovalReason != domain.RemovalRefund || !slices.Contains(e.max.Deleted(), msgID) {
		t.Fatalf("publication %+v", pub)
	}
	if p := e.placement(pid); p.Status != domain.StatusRefunded || p.RemovedAt == nil {
		t.Fatalf("after removal %s %v", p.Status, p.RemovedAt)
	}
	if e.count(`SELECT count(*) FROM violation WHERE placement_id = $1`, pid) != 0 {
		t.Fatal("own removal reported")
	}
}

func TestAdminOpenAndRemovePost(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// open из removed.
	pid := e.removed()
	_, err := e.svc.OpenDisputeByAdmin(ctx, pid, e.admin(), "")
	wantReason(t, err, apperr.CodeValidationFailed, "")
	d, err := e.svc.OpenDisputeByAdmin(ctx, pid, e.admin(), "жалоба в поддержку")
	if err != nil || d.OpenedBy != domain.OpenedByAdmin || e.placement(pid).Status != domain.StatusDisputed {
		t.Fatalf("open: %v %+v", err, d)
	}
	if s := e.jobStatus(domain.TimerKey(pid, domain.TimerClaim)); s != "cancelled" {
		t.Fatalf("T-05 %s", s)
	}
	if v, err := e.svc.AdminDispute(ctx, pid); err != nil || v.Publication == nil || v.Preview != nil || len(v.History) == 0 {
		t.Fatalf("view %v %+v", err, v)
	}
	// remove-post в removed — публикация уже снята.
	_, err = e.svc.RemovePostByAdmin(ctx, pid, e.admin(), "нарушение")
	wantReason(t, err, apperr.CodeConflict, "publication_not_live")

	// remove-post из published открывает спор и удаляет пост.
	e2 := newEnv(t)
	pid2, _, msgID := e2.published()
	r, err := e2.svc.RemovePostByAdmin(ctx, pid2, e2.admin(), "запрещённый контент")
	if err != nil || !r.Opened || e2.placement(pid2).Status != domain.StatusDisputed {
		t.Fatalf("remove-post: %v %+v", err, r)
	}
	e2.runJobs()
	if pub := e2.publication(pid2); pub.Live() || pub.RemovalReason != domain.RemovalAdmin || !slices.Contains(e2.max.Deleted(), msgID) {
		t.Fatalf("publication %+v", pub)
	}
	if p := e2.placement(pid2); p.Status != domain.StatusDisputed || p.RemovedAt == nil {
		t.Fatalf("placement %s %v", p.Status, p.RemovedAt)
	}
	// После снятия решение с выплатой допустимо.
	share := 3000
	if _, err := e2.resolve(pid2, domain.ResolutionProportional, &share); err != nil {
		t.Fatal(err)
	}
	rows, err := e2.svc.AdminDisputes(ctx, app.AdminDisputeFilter{All: true})
	if err != nil || len(rows) != 1 || rows[0].Dispute.Resolution != domain.ResolutionProportional {
		t.Fatalf("list %v %+v", err, rows)
	}
	if open, _ := e2.svc.AdminDisputes(ctx, app.AdminDisputeFilter{}); len(open) != 0 {
		t.Fatalf("open %+v", open)
	}
}
