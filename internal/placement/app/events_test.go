package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/inbound"
)

// event записывает событие MAX так, как это делает приём Webhook.
func (e *env) event(body string) {
	e.t.Helper()
	var head struct {
		UpdateType string `json:"update_type"`
	}
	if err := json.Unmarshal([]byte(body), &head); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.recorder.Record(context.Background(), inbound.Event{
		Source: inbound.SourceMAX, ExternalKey: inbound.KeyFromBody([]byte(body)), EventType: head.UpdateType, Payload: []byte(body),
	}); err != nil {
		e.t.Fatal(err)
	}
}

func removedEvent(chatID int64, msgID string, at time.Time) string {
	return fmt.Sprintf(`{"update_type":"message_removed","timestamp":%d,"message_id":%q,"chat_id":%d,"user_id":42}`,
		at.UnixMilli(), msgID, chatID)
}

func editedEvent(chatID int64, msgID, text string, at time.Time) string {
	return fmt.Sprintf(`{"update_type":"message_edited","timestamp":%d,"message":{"sender":{"user_id":42},`+
		`"recipient":{"chat_id":%d,"chat_type":"channel"},"timestamp":%d,"body":{"mid":%q,"text":%q}}}`,
		at.UnixMilli(), chatID, at.UnixMilli(), msgID, text)
}

// channelPost — сообщение человека в канале размещения, как его передаёт модуль каналов (R-9).
func (e *env) channelPost(pid uuid.UUID, sender int64, at time.Time) error {
	e.t.Helper()
	ev := ids.New()
	return e.svc.ChannelPost(context.Background(), domain.ChannelPost{
		ChannelID: e.placement(pid).ChannelID, MessageID: "mid.human." + ev.String()[:8], SenderID: sender, At: at,
		InboundEventID: &ev,
	})
}

// topViolation — чужое сообщение через 10 минут после публикации.
func (e *env) topViolation(pid uuid.UUID, _ int64) {
	e.t.Helper()
	if err := e.channelPost(pid, 42, e.placement(pid).PublishedAt.Add(10*time.Minute)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) dispute(pid uuid.UUID) domain.Dispute {
	e.t.Helper()
	d, err := e.store.Disputes().GetByPlacement(context.Background(), pid)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func TestTopViolation(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	published := *e.placement(pid).PublishedAt

	// Наш бот и сообщения вне топа нарушением не считаются.
	if err := e.channelPost(pid, 1, published.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := e.channelPost(pid, 42, published.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM violation`) != 0 {
		t.Fatal("own or late message reported")
	}

	ev := ids.New()
	post := domain.ChannelPost{ChannelID: e.placement(pid).ChannelID, MessageID: "mid.human", SenderID: 42,
		At: published.Add(20 * time.Minute), InboundEventID: &ev}
	for range 2 { // повтор того же события — одна запись
		if err := e.svc.ChannelPost(context.Background(), post); err != nil {
			t.Fatal(err)
		}
	}
	p := e.placement(pid)
	if p.Status != domain.StatusDisputed || e.count(`SELECT count(*) FROM violation WHERE kind = 'top_violated'`) != 1 {
		t.Fatalf("top: %s", p.Status)
	}
	d := e.dispute(pid)
	if d.Reason != "top_violated" || d.OpenedBy != domain.OpenedBySystem || d.SuggestedShare == nil || *d.SuggestedShare != 5000 ||
		!d.DeadlineAt.Equal(e.clk.Now().Add(72*time.Hour)) {
		t.Fatalf("dispute %+v", d)
	}
	if !e.jobRunAt(domain.TimerKey(pid, domain.TimerDispute)).Equal(d.DeadlineAt) {
		t.Fatal("T-06 not scheduled")
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND transition = 'P-14'
		AND inbound_event_id = $2`, pid, ev) != 1 {
		t.Fatal("P-14 history without event source")
	}
	if !slices.Contains(e.alerts.reasons(), "placement_disputed") || e.notices.Count(domain.NoticeDisputed) != 3 {
		t.Fatalf("alerts %v notices %v", e.alerts.reasons(), e.notices.Templates())
	}
	if len(e.logged("placement violation detected")) != 1 || len(e.logged("placement dispute opened")) != 1 {
		t.Fatal("violation not logged")
	}

	// Второе нарушение при открытом споре — только запись (I-11).
	if err := e.channelPost(pid, 43, published.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM violation`) != 2 || e.count(`SELECT count(*) FROM dispute`) != 1 {
		t.Fatal("second violation")
	}
	if len(e.logged("violation recorded, dispute exists")) != 1 {
		t.Fatal("second violation not logged")
	}

	// T-06: спор не решён — оповещение, состояние прежнее.
	e.clk.Set(d.DeadlineAt.Add(time.Second))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusDisputed || !slices.Contains(e.alerts.reasons(), "dispute_overdue") {
		t.Fatalf("T-06: %s alerts %v", p.Status, e.alerts.reasons())
	}
}

func TestRemovedEarlyByEvent(t *testing.T) {
	e := newEnv(t)
	pid, chatID, msgID := e.published()
	p := e.placement(pid)
	removedAt := p.PublishedAt.Add(18 * time.Hour)
	e.clk.Set(removedAt.Add(time.Minute))
	body := removedEvent(chatID, msgID, removedAt)
	e.event(body)
	e.event(body) // повтор Webhook
	e.runJobs()

	p = e.placement(pid)
	if p.Status != domain.StatusDisputed || p.StatusReason != "removed_early" {
		t.Fatalf("removed early: %s %s", p.Status, p.StatusReason)
	}
	pub := e.publication(pid)
	if pub.RemovalReason != domain.RemovalExternal || !pub.RemovedAt.Equal(removedAt) {
		t.Fatalf("publication %+v", pub)
	}
	if d := e.dispute(pid); d.SuggestedShare == nil || *d.SuggestedShare != 7500 {
		t.Fatalf("suggested share %v", d.SuggestedShare)
	}
	if e.jobStatus(domain.RemoveKey(pid)) != "cancelled" || e.count(`SELECT count(*) FROM violation`) != 1 {
		t.Fatal("removal job or violations")
	}
	// Снятие по сроку больше ничего не делает.
	e.clk.Set(p.RemoveAt.Add(time.Second))
	e.runJobs()
	if len(e.max.Deleted()) != 0 {
		t.Fatal("removed post deleted again")
	}
}

func TestContentModifiedByEvent(t *testing.T) {
	e := newEnv(t)
	pid, chatID, msgID := e.published()
	e.event(editedEvent(chatID, msgID, "Другой текст", e.clk.Now()))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusDisputed || p.StatusReason != "content_modified" {
		t.Fatalf("edited: %s %s", p.Status, p.StatusReason)
	}
	if d := e.dispute(pid); d.SuggestedShare != nil {
		t.Fatalf("content_modified suggestion %d", *d.SuggestedShare)
	}
	// Событие по чужому сообщению — ignored.
	e.event(editedEvent(chatID, "mid.unknown", "x", e.clk.Now()))
	e.runJobs()
	if e.count(`SELECT count(*) FROM inbound_event WHERE status = 'ignored'`) != 1 {
		t.Fatal("unknown message event not ignored")
	}
}

func TestBotAccessLostWhilePublished(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	ch := e.placement(pid).ChannelID
	if err := e.svc.ChannelUnavailable(e.ctx(), ch, "active", "suspended"); err != nil {
		t.Fatal(err)
	}
	if p := e.placement(pid); p.Status != domain.StatusDisputed || p.StatusReason != "bot_access_lost" {
		t.Fatalf("bot access lost: %s %s", p.Status, p.StatusReason)
	}
}
