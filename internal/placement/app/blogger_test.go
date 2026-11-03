package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

// otherAdvertiser — второй рекламодатель.
func (e *env) otherAdvertiser() (uuid.UUID, uuid.UUID) { return posttest.Advertiser(e.t, e.d.Pool) }

// sent — заказ одного канала, отправленный блогеру; возвращает размещение.
func (e *env) sent(ch uuid.UUID) uuid.UUID {
	e.t.Helper()
	o := e.order(ch)
	e.ready(o)
	return o.Placements[0].Placement.ID
}

func TestBloggerVisibility(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	pid := o.Placements[0].Placement.ID
	// До P-02 блогер размещение не видит.
	if _, err := e.svc.GetBloggerPlacement(context.Background(), blogger, pid); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("before P-02: %v", err)
	}
	if page, _ := e.svc.ListBloggerPlacements(context.Background(), blogger, nil, nil, "", 10); len(page.Items) != 0 {
		t.Fatal("listed before P-02")
	}
	e.ready(o)
	v, err := e.svc.GetBloggerPlacement(context.Background(), blogger, pid)
	if err != nil {
		t.Fatal(err)
	}
	if v.Post == nil || !strings.Contains(v.Post.FinalText, "erid") || v.Post.Erid == "" || v.OrderNumber != o.Order.Number ||
		v.AdvertiserName != posttest.Name || strings.Join(v.Actions, ",") != "accept,decline" || v.Channel.Title != "Канал" {
		t.Fatalf("blogger view %+v", v)
	}
	stranger, _ := e.otherAdvertiser()
	if _, err := e.svc.GetBloggerPlacement(context.Background(), stranger, pid); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign blogger: %v", err)
	}
	// После P-11 размещение видно в created, но без поста и действий.
	if _, err := e.svc.Accept(e.ctx(), blogger, user, pid, ""); err != nil {
		t.Fatal(err)
	}
	e.saveDraft(o.Order.CampaignID, "Новый текст")
	if _, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, ""); err != nil {
		t.Fatal(err)
	}
	v, err = e.svc.GetBloggerPlacement(context.Background(), blogger, pid)
	if err != nil || v.Placement.Status != domain.StatusCreated || v.Post != nil || len(v.Actions) != 0 {
		t.Fatalf("after P-11: %+v %v", v, err)
	}
	st := domain.StatusCreated
	if page, _ := e.svc.ListBloggerPlacements(context.Background(), blogger, &st, &ch, "", 10); len(page.Items) != 1 {
		t.Fatal("filtered list")
	}
}

func TestAcceptChecks(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	pid := e.sent(ch)
	e.contractors.Deny(blogger, true)
	_, err := e.svc.Accept(e.ctx(), blogger, user, pid, "")
	wantReason(t, err, apperr.CodeConflict, "contractor_inactive")
	e.contractors.Deny(blogger, false)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'suspended' WHERE id = $1`, ch); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Accept(e.ctx(), blogger, user, pid, "")
	wantReason(t, err, apperr.CodeConflict, "channel_not_active")
	if len(e.logged("placement accept blocked")) != 2 {
		t.Fatal("accept blocked not logged")
	}
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'active', ord_platform_ready_at = now() WHERE id = $1`, ch); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Accept(e.ctx(), blogger, user, pid, "acc-1")
	if err != nil || v.Placement.Status != domain.StatusAwaitingSchedule || strings.Join(v.Actions, ",") != "schedule" {
		t.Fatalf("accept: %+v %v", v.Placement, err)
	}
	if e.jobStatus(domain.TimerKey(pid, domain.TimerAccept)) != "cancelled" || e.jobStatus(domain.TimerKey(pid, domain.TimerSchedule)) != "pending" {
		t.Fatal("T-01/T-13 jobs")
	}
	if v, err := e.svc.Accept(e.ctx(), blogger, user, pid, "acc-1"); err != nil || v.Placement.Status != domain.StatusAwaitingSchedule {
		t.Fatalf("repeat accept with key: %v", err)
	}
	_, err = e.svc.Accept(e.ctx(), blogger, user, pid, "")
	wantReason(t, err, apperr.CodeInvalidTransition, "")
}

func TestDecline(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	pid := e.sent(ch)
	const comment = "Не подходит тематика канала"
	v, err := e.svc.Decline(e.ctx(), blogger, user, pid, comment, "")
	if err != nil || v.Placement.Status != domain.StatusDeclined || e.holds.Active(pid) {
		t.Fatalf("decline: %+v %v", v.Placement, err)
	}
	var text string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT reason_text FROM placement_state_history WHERE placement_id = $1
		AND transition = 'P-04'`, pid).Scan(&text); err != nil || text != comment {
		t.Fatalf("history reason text %q %v", text, err)
	}
	if strings.Contains(e.logs.String(), comment) {
		t.Fatal("blogger comment in application log")
	}
	if e.notices.Count(domain.NoticeDeclined) != 1 {
		t.Fatal("decline notice")
	}
}

// accepted — размещение в awaiting_schedule.
func (e *env) accepted(blogger, user, ch uuid.UUID) uuid.UUID {
	e.t.Helper()
	pid := e.sent(ch)
	if _, err := e.svc.Accept(e.ctx(), blogger, user, pid, ""); err != nil {
		e.t.Fatal(err)
	}
	return pid
}

func ptr[T any](v T) *T { return &v }

func TestSchedule(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	pid := e.accepted(blogger, user, ch)
	// Интервал недоступности 20 октября 14:00–16:00 МСК.
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO channel_unavailability (id, channel_id, starts_at, ends_at, created_at)
		VALUES ($1, $2, $3, $4, now())`, ids.New(), ch, msk(20, 14, 0), msk(20, 16, 0)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode   domain.ScheduleMode
		at     *time.Time
		reason string
	}{
		{domain.ModeAt, nil, "publish_at_required"},
		{domain.ModeAt, ptr(msk(23, 10, 0)), "outside_window"},
		{domain.ModeAt, ptr(msk(20, 8, 0)), "outside_time"},
		{domain.ModeAt, ptr(msk(20, 12, 7)), "not_aligned"},
		{domain.ModeAt, ptr(msk(20, 15, 0)), "channel_unavailable_interval"},
		{domain.ModeNow, nil, "outside_window"}, // 19 октября — вне периода дат
	} {
		_, err := e.svc.Schedule(e.ctx(), blogger, user, pid, tc.mode, tc.at, "")
		wantReason(t, err, apperr.CodeValidationFailed, tc.reason)
	}
	e.clk.Set(msk(20, 11, 30))
	_, err := e.svc.Schedule(e.ctx(), blogger, user, pid, domain.ModeAt, ptr(msk(20, 12, 0)), "")
	wantReason(t, err, apperr.CodeValidationFailed, "too_soon")
	if len(e.logged("placement schedule rejected")) != 7 {
		t.Fatalf("schedule rejected logged %d", len(e.logged("placement schedule rejected")))
	}

	// «Опубликовать сейчас»: publish_placement на текущий момент.
	v, err := e.svc.Schedule(e.ctx(), blogger, user, pid, domain.ModeNow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	now := e.clk.Now()
	if v.Placement.Status != domain.StatusScheduled || !v.Placement.PublishAt.Equal(now) || !v.Placement.RemoveAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("now: %+v", v.Placement)
	}
	if !e.jobRunAt(domain.PublishKey(pid)).Equal(now) || !e.jobRunAt(domain.RemoveKey(pid)).Equal(now.Add(24*time.Hour)) ||
		e.jobStatus(domain.TimerKey(pid, domain.TimerSchedule)) != "cancelled" {
		t.Fatal("publish/remove jobs")
	}
	if e.notices.Count(domain.NoticeScheduled) != 1 {
		t.Fatal("scheduled notice")
	}
}

func TestReschedule(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	pid := e.accepted(blogger, user, ch)
	if _, err := e.svc.Schedule(e.ctx(), blogger, user, pid, domain.ModeAt, ptr(msk(20, 12, 0)), ""); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Reschedule(e.ctx(), blogger, user, pid, msk(21, 10, 0), "")
	if err != nil || !v.Placement.PublishAt.Equal(msk(21, 10, 0)) {
		t.Fatalf("reschedule: %+v %v", v.Placement, err)
	}
	if !e.jobRunAt(domain.PublishKey(pid)).Equal(msk(21, 10, 0)) || !e.jobRunAt(domain.RemoveKey(pid)).Equal(msk(22, 10, 0)) {
		t.Fatal("jobs not moved")
	}
	// За 50 минут до публикации перенос недоступен.
	e.clk.Set(msk(21, 9, 10))
	_, err = e.svc.Reschedule(e.ctx(), blogger, user, pid, msk(21, 15, 0), "")
	wantReason(t, err, apperr.CodeInvalidTransition, "too_late")
	other, _ := e.otherAdvertiser()
	if _, err := e.svc.Reschedule(e.ctx(), other, user, pid, msk(21, 15, 0), ""); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign blogger: %v", err)
	}
}

func TestParallelScheduleSameChannel(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	p1 := e.accepted(blogger, user, ch)
	p2 := e.accepted(blogger, user, ch)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pid := range []uuid.UUID{p1, p2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Топы 12:00–13:00 и 12:30–13:30 пересекаются (I-06).
			at := msk(20, 12, 0).Add(time.Duration(i) * 30 * time.Minute)
			_, errs[i] = e.svc.Schedule(e.ctx(), blogger, user, pid, domain.ModeAt, &at, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		default:
			wantReason(t, err, apperr.CodeValidationFailed, "slot_taken")
		}
	}
	if ok != 1 || e.count(`SELECT count(*) FROM placement WHERE status = 'scheduled'`) != 1 {
		t.Fatalf("scheduled %d", ok)
	}
}
