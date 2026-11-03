package app_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

// Напоминание о T-01 (R-8 этапа уведомлений): P-02 ставит его, ответ блогера отменяет.
func TestAcceptReminderJob(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	pid := e.sent(ch)
	p := e.placement(pid)
	key := domain.ReminderKey(pid)
	if !e.jobRunAt(key).Equal(p.AcceptDeadlineAt.Add(-domain.ReminderLead)) {
		t.Fatalf("reminder at %v, deadline %v", e.jobRunAt(key), p.AcceptDeadlineAt)
	}
	e.clk.Set(e.jobRunAt(key))
	e.runJobs()
	if e.notices.Count(domain.NoticeAcceptReminder) != 1 || len(e.logged("placement reminder sent")) != 1 {
		t.Fatalf("notices %v", e.notices.Templates())
	}

	// Второй запрос принят до напоминания — задание отменено.
	blogger2, user2, ch2 := e.blogger("Второй", 1_000_000)
	pid2 := e.sent(ch2)
	if _, err := e.svc.Accept(e.ctx(), blogger2, user2, pid2, ""); err != nil {
		t.Fatal(err)
	}
	if st := e.jobStatus(domain.ReminderKey(pid2)); st != "cancelled" {
		t.Fatalf("reminder after accept: %q", st)
	}
}
