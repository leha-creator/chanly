package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var seq atomic.Int64

// fx — данные теста: рекламодатель, блогер с каналом, хранилище модуля.
type fx struct {
	t       *testing.T
	d       *dbtest.DB
	s       *Store
	adv     uuid.UUID
	user    uuid.UUID
	blogger uuid.UUID
	channel uuid.UUID
}

func newFx(t *testing.T) *fx {
	d := dbtest.New(t)
	f := &fx{t: t, d: d, s: New(d.Pool, logx.Discard())}
	f.adv = f.org("legal_entity")
	f.user = f.newUser()
	f.blogger = f.org("self_employed")
	f.channel = f.newChannel(f.blogger)
	return f
}

func (f *fx) exec(stmt string, args ...any) {
	f.t.Helper()
	if _, err := f.d.Pool.Exec(context.Background(), stmt, args...); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
}

func (f *fx) org(legalType string) uuid.UUID {
	id := ids.New()
	inn := fmt.Sprintf("77%010d", seq.Add(1)) // ИП и самозанятый — 12 цифр
	if legalType == "legal_entity" {
		inn = fmt.Sprintf("77%08d", seq.Add(1)) // юрлицо — 10 цифр
	}
	f.exec(`INSERT INTO organization (id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'Организация', 'Контакт', '+79990000000', 'active', now(), now())`, id, legalType, inn)
	return id
}

func (f *fx) newUser() uuid.UUID {
	id := ids.New()
	f.exec(`INSERT INTO "user" (id, email, status, created_at, updated_at) VALUES ($1, $2, 'active', now(), now())`,
		id, id.String()+"@example.test")
	return id
}

func (f *fx) newChannel(owner uuid.UUID) uuid.UUID {
	id := ids.New()
	f.exec(`INSERT INTO channel (id, organization_id, max_chat_id, added_by_max_user_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, 7, 'moderation', now(), now())`, id, owner, -1000-seq.Add(1))
	return id
}

// campaign — кампания с постом и версией 1.
func (f *fx) campaign() (campaign, post, version uuid.UUID) {
	campaign, post, version = ids.New(), ids.New(), ids.New()
	f.exec(`INSERT INTO campaign (id, organization_id, name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, 'Кампания', 'draft', $3, now(), now())`, campaign, f.adv, f.user)
	f.exec(`INSERT INTO post (id, organization_id, campaign_id, status, draft, created_at, updated_at)
		VALUES ($1, $2, $3, 'draft', '{}', now(), now())`, post, f.adv, campaign)
	f.exec(`INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier, token_position,
		advertiser_name, advertiser_inn, created_by, created_at)
		VALUES ($1, $2, 1, 'pending_moderation', '{}', 'текст', 'text', 'below', 'ООО', '7707083893', $3, now())`, version, post, f.user)
	return campaign, post, version
}

func (f *fx) order(created time.Time) domain.Order {
	f.t.Helper()
	ctx := context.Background()
	campaign, post, version := f.campaign()
	n, err := f.s.Orders().NextNumber(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	o := domain.Order{ID: ids.New(), Number: n, OrganizationID: f.adv, CampaignID: campaign, PostID: post,
		CurrentPostVersionID: version, Status: domain.OrderModeration, TotalAmount: 1_150_000, CreatedBy: f.user,
		CreatedAt: created, UpdatedAt: created}
	if err := f.s.Orders().Insert(ctx, o); err != nil {
		f.t.Fatal(err)
	}
	return o
}

func (f *fx) placement(o domain.Order, channel uuid.UUID, status domain.Status, created time.Time) domain.Placement {
	f.t.Helper()
	p := domain.Placement{
		ID: ids.New(), OrderID: o.ID, ChannelID: channel, BloggerOrgID: f.blogger, FormatID: "f_1_24", TopHours: 1,
		DurationHours: 24, WindowStart: created, WindowEnd: created.Add(72 * time.Hour), TimeFrom: 9 * 60, TimeTo: 21 * 60,
		PostVersionID: o.CurrentPostVersionID, Status: status, StatusChangedAt: created, FeeRateBP: 1500,
		BloggerAmount: 1_000_000, FeeAmount: 150_000, TotalAmount: 1_150_000, LockVersion: 1, CreatedAt: created, UpdatedAt: created,
	}
	if status != domain.StatusCreated {
		p.SentToBloggerAt = &created
	}
	if status == domain.StatusScheduled || status == domain.StatusPublished {
		pub, rem := created.Add(2*time.Hour), created.Add(26*time.Hour)
		p.PublishAt, p.RemoveAt = &pub, &rem
	}
	if err := f.s.Placements().Insert(context.Background(), p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)

func TestPlacementUpdateIfStatus(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	p := f.placement(f.order(t0), f.channel, domain.StatusCreated, t0)
	repo := f.s.Placements()

	got, err := repo.GetForUpdate(ctx, p.ID)
	if err != nil || got.TimeFrom != 9*60 || got.TimeTo != 21*60 || got.TotalAmount != 1_150_000 {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	change, err := got.SendToBlogger(true, true, domain.VersionReady, t0.Add(time.Hour), domain.DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIfStatus(ctx, got, change.From, change.PrevLockVersion); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Повтор с устаревшими ожиданиями — ErrStaleVersion (03 §11).
	if err := repo.UpdateIfStatus(ctx, got, change.From, change.PrevLockVersion); !errors.Is(err, domain.ErrStaleVersion) {
		t.Fatalf("stale update: %v", err)
	}
	// Из конечного состояния — 500 invariant_violated от триггера (I-04).
	cancelled, _ := repo.GetForUpdate(ctx, p.ID)
	change, _ = cancelled.Cancel(domain.ActorSystem, domain.ReasonChannelUnavailable, t0.Add(2*time.Hour))
	if err := repo.UpdateIfStatus(ctx, cancelled, change.From, change.PrevLockVersion); err != nil {
		t.Fatal(err)
	}
	cancelled.Status = domain.StatusCreated
	err = repo.UpdateIfStatus(ctx, cancelled, domain.StatusCancelled, cancelled.LockVersion)
	if e, ok := apperr.As(err); !ok || e.Code != apperr.CodeInternal || e.Details["reason"] != "invariant_violated" {
		t.Fatalf("leave terminal: %v", err)
	}
}

func TestOrderAndBloggerCursors(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	var orders []domain.Order
	for range 5 {
		orders = append(orders, f.order(t0)) // одинаковое created_at
	}
	var seen []uuid.UUID
	var after *domain.PageCursor
	for {
		page, err := f.s.Orders().List(ctx, f.adv, nil, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range page {
			seen = append(seen, o.ID)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 5 || !unique(seen) {
		t.Fatalf("orders by cursor: %v", seen)
	}
	other := f.org("legal_entity")
	if list, _ := f.s.Orders().List(ctx, other, nil, nil, 10); len(list) != 0 {
		t.Fatal("another organization sees orders")
	}

	var placements []uuid.UUID
	for _, o := range orders {
		placements = append(placements, f.placement(o, f.channel, domain.StatusAwaitingBlogger, t0).ID)
	}
	f.placement(f.order(t0), f.channel, domain.StatusCreated, t0) // не отправлено блогеру
	seen = nil
	after = nil
	for {
		page, err := f.s.Placements().ListForBlogger(ctx, f.blogger, nil, nil, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			seen = append(seen, p.ID)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != len(placements) || !unique(seen) {
		t.Fatalf("blogger placements by cursor: %d of %d", len(seen), len(placements))
	}
	st := domain.StatusCreated
	if list, _ := f.s.Placements().ListForBlogger(ctx, f.blogger, &st, nil, nil, 10); len(list) != 0 {
		t.Fatal("blogger sees placement in created before P-02")
	}
}

func unique(ids []uuid.UUID) bool {
	m := map[uuid.UUID]bool{}
	for _, id := range ids {
		if m[id] {
			return false
		}
		m[id] = true
	}
	return true
}

func TestPlacementAccess(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	o := f.order(t0)
	created := f.placement(o, f.channel, domain.StatusCreated, t0)
	other := f.org("legal_entity")
	if _, err := f.s.Placements().GetForAdvertiserForUpdate(ctx, other, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign advertiser: %v", err)
	}
	if _, err := f.s.Placements().GetForAdvertiserForUpdate(ctx, f.adv, created.ID); err != nil {
		t.Fatalf("own advertiser: %v", err)
	}
	if _, err := f.s.Placements().GetForBlogger(ctx, f.blogger, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("blogger before P-02: %v", err)
	}
	sent := f.placement(f.order(t0), f.channel, domain.StatusAwaitingBlogger, t0)
	if _, err := f.s.Placements().GetForBlogger(ctx, f.blogger, sent.ID); err != nil {
		t.Fatalf("blogger after P-02: %v", err)
	}
	if _, err := f.s.Placements().GetForBloggerForUpdate(ctx, other, sent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign blogger: %v", err)
	}
	if _, err := f.s.Orders().Get(ctx, other, o.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign order: %v", err)
	}
	// Второе размещение того же канала в заказе — 409.
	dup := created
	dup.ID = ids.New()
	if err := f.s.Placements().Insert(ctx, dup); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("duplicate channel in order: %v", err)
	}
}

func TestBusyIntervalsAndCounts(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	repo := f.s.Placements()
	scheduled := f.placement(f.order(t0), f.channel, domain.StatusScheduled, t0)
	published := f.placement(f.order(t0.Add(time.Hour)), f.channel, domain.StatusPublished, t0.Add(time.Hour))
	f.placement(f.order(t0), f.channel, domain.StatusAwaitingSchedule, t0)
	f.placement(f.order(t0), f.newChannel(f.blogger), domain.StatusScheduled, t0) // другой канал

	busy, err := repo.BusyIntervals(ctx, f.channel, t0, t0.Add(48*time.Hour), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(busy) != 2 || !busy[0].Start.Equal(*scheduled.PublishAt) || !busy[1].Start.Equal(*published.PublishAt) ||
		!busy[0].End.Equal(scheduled.PublishAt.Add(time.Hour)) {
		t.Fatalf("busy %+v", busy)
	}
	if busy, _ := repo.BusyIntervals(ctx, f.channel, t0, t0.Add(48*time.Hour), scheduled.ID); len(busy) != 1 {
		t.Fatalf("exclude self: %+v", busy)
	}
	if busy, _ := repo.BusyIntervals(ctx, f.channel, t0.Add(4*time.Hour), t0.Add(48*time.Hour), uuid.Nil); len(busy) != 0 {
		t.Fatalf("tops ended before from: %+v", busy)
	}
	ids, err := repo.ScheduledOverlapping(ctx, f.channel, t0.Add(10*time.Hour), t0.Add(11*time.Hour))
	if err != nil || len(ids) != 1 || ids[0] != scheduled.ID {
		t.Fatalf("scheduled overlapping: %v %v", ids, err)
	}
	counts, err := repo.CountByStatus(ctx)
	if err != nil || counts[domain.StatusScheduled] != 2 || counts[domain.StatusPublished] != 1 || counts[domain.StatusAwaitingSchedule] != 1 {
		t.Fatalf("counts %v %v", counts, err)
	}
	overdue, err := repo.OverdueScheduled(ctx, t0.Add(3*time.Hour))
	if err != nil || len(overdue) != 2 {
		t.Fatalf("overdue %v %v", overdue, err)
	}
}

func TestIdempotencyKeysAndCart(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	o := f.order(t0)
	p := f.placement(o, f.channel, domain.StatusCreated, t0)
	h := domain.HistoryEntry{ID: ids.New(), PlacementID: p.ID, Transition: domain.P06Cancel, From: domain.StatusCreated,
		To: domain.StatusCancelled, ActorType: domain.ActorAdvertiser, ActorUserID: &f.user, ReasonCode: domain.ReasonAdvertiserCancel,
		IdempotencyKey: "k-1", CreatedAt: t0}
	if err := f.s.History().Insert(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, ok, err := f.s.History().FindByKey(ctx, p.ID, "k-1")
	if err != nil || !ok || got.ID != h.ID || got.From != domain.StatusCreated || *got.ActorUserID != f.user {
		t.Fatalf("find by key: %v %v %+v", err, ok, got)
	}
	if _, ok, err := f.s.History().FindByKey(ctx, p.ID, "k-2"); ok || err != nil {
		t.Fatalf("unknown key: %v %v", ok, err)
	}
	if ok, err := f.s.History().ExistsKeyInOrder(ctx, o.ID, "k-1"); !ok || err != nil {
		t.Fatalf("key in order: %v %v", ok, err)
	}
	h2 := h
	h2.ID = ids.New()
	if err := f.s.History().Insert(ctx, h2); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("repeated key: %v", err)
	}

	campaign, post, version := f.campaign()
	keyed := domain.Order{ID: ids.New(), Number: 5000, OrganizationID: f.adv, CampaignID: campaign, PostID: post,
		CurrentPostVersionID: version, Status: domain.OrderModeration, IdempotencyKey: "checkout-1", CreatedBy: f.user,
		CreatedAt: t0, UpdatedAt: t0}
	if err := f.s.Orders().Insert(ctx, keyed); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := f.s.Orders().FindByIdempotencyKey(ctx, f.adv, "checkout-1"); err != nil || !ok || got.ID != keyed.ID {
		t.Fatalf("order by key: %v %v", ok, err)
	}
	if got, ok, err := f.s.Orders().FindByCurrentVersion(ctx, version); err != nil || !ok || got.ID != keyed.ID {
		t.Fatalf("order by version: %v %v", ok, err)
	}

	carts := f.s.Carts()
	item := domain.CartItem{ID: ids.New(), OrganizationID: f.adv, ChannelID: f.channel, FormatID: "f_1_24",
		ShownTotal: 1_150_000, CreatedBy: f.user, CreatedAt: t0, UpdatedAt: t0}
	if err := carts.Insert(ctx, item); err != nil {
		t.Fatal(err)
	}
	item2 := item
	item2.ID = ids.New()
	if err := carts.Insert(ctx, item2); !errors.Is(err, domain.ErrAlreadyInCart) {
		t.Fatalf("duplicate channel: %v", err)
	}
	in, err := carts.ChannelsInCart(ctx, f.adv, []uuid.UUID{f.channel, uuid.New()})
	if err != nil || !in[f.channel] || len(in) != 1 {
		t.Fatalf("in cart %v %v", in, err)
	}
	other := f.org("legal_entity")
	if err := carts.Delete(ctx, other, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if n, _ := carts.Count(ctx, f.adv); n != 1 {
		t.Fatalf("count %d", n)
	}
	if err := carts.Clear(ctx, f.adv); err != nil {
		t.Fatal(err)
	}
	if list, _ := carts.List(ctx, f.adv); len(list) != 0 {
		t.Fatal("cart not cleared")
	}
}
