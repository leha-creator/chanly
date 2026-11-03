package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func (f *fx) publication(p domain.Placement, msgID string, at time.Time) domain.Publication {
	f.t.Helper()
	pub := domain.NewPublication(ids.New(), &p, domain.SentMessage{ID: msgID, URL: "https://max.ru/" + msgID, ContentHash: "h"}, at, at)
	if err := f.s.Publications().Insert(context.Background(), pub); err != nil {
		f.t.Fatal(err)
	}
	return pub
}

func TestPublicationsRepository(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	o := f.order(t0)
	p1 := f.placement(o, f.channel, domain.StatusPublished, t0)
	other := f.newChannel(f.blogger)
	p2 := f.placement(o, other, domain.StatusPublished, t0)
	p3 := f.placement(f.order(t0), f.channel, domain.StatusRemoved, t0)
	pub1 := f.publication(p1, "mid.1", t0)
	f.publication(p2, "mid.2", t0)
	pub3 := f.publication(p3, "mid.3", t0)

	// I-11: вторая публикация размещения.
	dup := domain.NewPublication(ids.New(), &p1, domain.SentMessage{ID: "mid.9", ContentHash: "h"}, t0, t0)
	if err := f.s.Publications().Insert(ctx, dup); !errors.Is(err, domain.ErrPublicationExists) {
		t.Fatalf("second publication: %v", err)
	}
	got, ok, err := f.s.Publications().FindByMessageID(ctx, "mid.1")
	if err != nil || !ok || got.ID != pub1.ID || got.URL != "https://max.ru/mid.1" {
		t.Fatalf("by message: %+v %v %v", got, ok, err)
	}
	if _, ok, err := f.s.Publications().FindByMessageID(ctx, "mid.none"); err != nil || ok {
		t.Fatalf("unknown message: %v %v", ok, err)
	}

	// Снятие третьей: она больше не живая.
	pub3.MarkRemoving(t0.Add(time.Hour))
	if err := f.s.Publications().Update(ctx, pub3); err != nil {
		t.Fatal(err)
	}
	var seen []uuid.UUID
	after := uuid.Nil
	for {
		page, err := f.s.Publications().ListLive(ctx, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		seen = append(seen, page[0].ID)
		after = page[0].ID
	}
	if len(seen) != 2 || !unique(seen) {
		t.Fatalf("live pages %v", seen)
	}

	live, err := f.s.Publications().ListLiveByChannel(ctx, f.channel)
	if err != nil || len(live) != 1 || live[0].Placement.ID != p1.ID || live[0].Publication.ID != pub1.ID {
		t.Fatalf("live by channel %+v %v", live, err)
	}

	// removal_overdue по порогу.
	got, _ = f.s.Publications().GetByPlacementForUpdate(ctx, p1.ID)
	got.RollbackRemoving(t0.Add(2 * time.Hour))
	if err := f.s.Publications().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if ids, err := f.s.Publications().OverdueRemovals(ctx, t0.Add(2*time.Hour+time.Minute)); err != nil || len(ids) != 1 || ids[0] != pub1.ID {
		t.Fatalf("overdue %v %v", ids, err)
	}
	if ids, _ := f.s.Publications().OverdueRemovals(ctx, t0.Add(time.Hour)); len(ids) != 0 {
		t.Fatalf("overdue before threshold %v", ids)
	}
	byP, err := f.s.Publications().ListByPlacements(ctx, []uuid.UUID{p1.ID, p2.ID})
	if err != nil || len(byP) != 2 {
		t.Fatalf("by placements %v %v", byP, err)
	}
}

func TestViolationsAndDisputes(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	p := f.placement(f.order(t0), f.channel, domain.StatusPublished, t0)
	ev := ids.New()
	v := domain.Violation{ID: ids.New(), PlacementID: p.ID, Kind: domain.ViolationTopViolated, OccurredAt: t0,
		Source: domain.SourceEvent, InboundEventID: &ev, Details: map[string]any{"message_id": "mid.h"}, CreatedAt: t0}
	if err := f.s.Violations().Insert(ctx, v); err != nil {
		t.Fatal(err)
	}
	v2 := v
	v2.ID = ids.New()
	if err := f.s.Violations().Insert(ctx, v2); !errors.Is(err, domain.ErrDuplicateViolation) {
		t.Fatalf("same event again: %v", err)
	}
	// Сверка без события — отдельная запись.
	v3 := v2
	v3.InboundEventID, v3.Source = nil, domain.SourceReconciliation
	if err := f.s.Violations().Insert(ctx, v3); err != nil {
		t.Fatal(err)
	}

	share := 5000
	d := domain.NewSystemDispute(ids.New(), v, &share, t0, domain.DefaultControlRules)
	if err := f.s.Disputes().Insert(ctx, d); err != nil {
		t.Fatal(err)
	}
	d2 := domain.NewSystemDispute(ids.New(), v3, nil, t0, domain.DefaultControlRules)
	if err := f.s.Disputes().Insert(ctx, d2); !errors.Is(err, domain.ErrDisputeExists) {
		t.Fatalf("second dispute: %v", err)
	}
	got, err := f.s.Disputes().GetByPlacement(ctx, p.ID)
	if err != nil || got.Reason != "top_violated" || got.OpenedBy != domain.OpenedBySystem || *got.SuggestedShare != 5000 ||
		!got.DeadlineAt.Equal(t0.Add(72*time.Hour)) || *got.ViolationID != v.ID {
		t.Fatalf("dispute %+v %v", got, err)
	}
	if m, err := f.s.Disputes().ListByPlacements(ctx, []uuid.UUID{p.ID}); err != nil || len(m) != 1 {
		t.Fatalf("list %v %v", m, err)
	}
}

func TestScheduledWithoutPublication(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	o := f.order(t0)
	p := f.placement(o, f.channel, domain.StatusScheduled, t0)
	refs, err := f.s.Placements().ScheduledWithoutPublication(ctx, t0.Add(3*time.Hour))
	if err != nil || len(refs) != 1 || refs[0].ID != p.ID || !refs[0].PublishAt.Equal(*p.PublishAt) {
		t.Fatalf("refs %+v %v", refs, err)
	}
	if refs, _ := f.s.Placements().ScheduledWithoutPublication(ctx, t0.Add(time.Hour)); len(refs) != 0 {
		t.Fatalf("before publish_at %+v", refs)
	}
	// История хранит входящее событие (03 §12).
	ev := ids.New()
	h := domain.HistoryEntry{ID: ids.New(), PlacementID: p.ID, Transition: domain.P14OpenDispute, From: domain.StatusPublished,
		To: domain.StatusDisputed, ActorType: domain.ActorSystem, InboundEventID: &ev, CreatedAt: t0}
	if err := f.s.History().Insert(ctx, h); err != nil {
		t.Fatal(err)
	}
	list, err := f.s.History().ListByPlacements(ctx, []uuid.UUID{p.ID})
	if err != nil || len(list[p.ID]) != 1 || list[p.ID][0].InboundEventID == nil || *list[p.ID][0].InboundEventID != ev {
		t.Fatalf("history %+v %v", list, err)
	}
	// Итоговые просмотры сохраняются обновлением размещения.
	views := 900
	p.FinalViews = &views
	p.LockVersion++
	if err := f.s.Placements().UpdateIfStatus(ctx, p, p.Status, p.LockVersion-1); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.Placements().Get(ctx, p.ID); got.FinalViews == nil || *got.FinalViews != 900 {
		t.Fatalf("final views %v", got.FinalViews)
	}
}
