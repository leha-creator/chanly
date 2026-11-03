package app_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestClaimFromPublished(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	v, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{
		Reason: domain.ClaimTopViolated, Text: "Чужой пост через 10 минут", IdemKey: "claim-1"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Placement.Status != domain.StatusDisputed || v.Claim == nil || v.Claim.Reason != domain.ClaimTopViolated ||
		v.Dispute == nil || v.Dispute.OpenedBy != domain.OpenedByAdvertiser {
		t.Fatalf("view %+v", v)
	}
	if slices.Contains(v.Actions, domain.ActionClaim) {
		t.Fatalf("claim action after claim: %v", v.Actions)
	}
	d := e.dispute(pid)
	if d.SuggestedShare == nil || *d.SuggestedShare != 5000 || *d.ClaimID != v.Claim.ID {
		t.Fatalf("dispute %+v", d)
	}
	if !e.jobRunAt(domain.TimerKey(pid, domain.TimerDispute)).Equal(e.clk.Now().Add(72 * time.Hour)) {
		t.Fatal("T-06 not scheduled")
	}
	if e.notices.Count(domain.NoticeDisputed) != 3 {
		t.Fatalf("notices %v", e.notices.Templates())
	}
	if r := e.alerts.reasons(); !slices.Contains(r, "placement_disputed") {
		t.Fatalf("alerts %v", r)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'placement.claim' AND object_id = $1`, pid.String()) != 1 {
		t.Fatal("audit")
	}
	// Текст претензии в журнал не попадает.
	if strings.Contains(e.logs.String(), "Чужой пост") {
		t.Fatal("claim text logged")
	}
	// Повтор с ключом — то же размещение, второй претензии нет.
	if _, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{
		Reason: domain.ClaimTopViolated, Text: "x", IdemKey: "claim-1"}); err != nil {
		t.Fatal(err)
	}
	// Без ключа — claim_exists.
	_, err = e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{Reason: domain.ClaimOther, Text: "x"})
	wantReason(t, err, apperr.CodeConflict, "claim_exists")
	if e.count(`SELECT count(*) FROM claim`) != 1 {
		t.Fatal("second claim")
	}
}

func TestClaimFromRemovedAndWindow(t *testing.T) {
	e := newEnv(t)
	pid := e.removed()
	p := e.placement(pid)
	e.clk.Set(p.ClaimDeadlineAt.Add(-time.Second))
	v, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{Reason: domain.ClaimContentMismatch, Text: "не тот текст"})
	if err != nil || v.Placement.Status != domain.StatusDisputed {
		t.Fatalf("claim from removed: %v", err)
	}
	if s := e.jobStatus(domain.TimerKey(pid, domain.TimerClaim)); s != "cancelled" {
		t.Fatalf("T-05 %s", s)
	}
	if d := e.dispute(pid); d.SuggestedShare != nil {
		t.Fatalf("suggestion for content_mismatch %v", *d.SuggestedShare)
	}
}

func TestClaimWindowClosed(t *testing.T) {
	e := newEnv(t)
	late := e.removed()
	e.clk.Set(*e.placement(late).ClaimDeadlineAt)
	_, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, late, app.ClaimInput{Reason: domain.ClaimOther, Text: "поздно"})
	wantReason(t, err, apperr.CodeInvalidTransition, domain.ReasonClaimWindowClosed)
	// Пустой текст — 422.
	_, err = e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, late, app.ClaimInput{Reason: domain.ClaimOther, Text: "  "})
	wantReason(t, err, apperr.CodeValidationFailed, "")
}

func TestClaimDuringSystemDispute(t *testing.T) {
	e := newEnv(t)
	pid, chatID, _ := e.published()
	e.topViolation(pid, chatID)
	_, err := e.svc.CreateClaim(e.ctx(), e.adv, e.advUser, pid, app.ClaimInput{Reason: domain.ClaimOther, Text: "x"})
	wantReason(t, err, apperr.CodeInvalidTransition, "")
	if e.count(`SELECT count(*) FROM claim`) != 0 {
		t.Fatal("claim inserted")
	}
}
