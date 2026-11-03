package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// schedule — поддельный модуль размещений: занятые интервалы, конфликт и корзина.
type schedule struct {
	conflict bool
	cart     map[uuid.UUID]bool
	calls    int
}

func (s *schedule) BusyIntervals(context.Context, uuid.UUID, time.Time, time.Time) ([]domain.Unavailability, error) {
	return nil, nil
}

func (s *schedule) ScheduledConflict(context.Context, uuid.UUID, time.Time, time.Time) (bool, error) {
	s.calls++
	return s.conflict, nil
}

func (s *schedule) InCart(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if s.cart[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestLockChannelForPlacementWaits(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000101)
	txr := tx.NewRunner(e.d.Pool, logx.Discard())
	locked := make(chan struct{})
	var mu sync.Mutex
	var committedAt, secondAt time.Time
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = txr.InTx(context.Background(), func(ctx context.Context) error {
			if _, err := e.svc.LockChannelForPlacement(ctx, c.ID); err != nil {
				t.Error(err)
			}
			close(locked)
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			committedAt = time.Now()
			mu.Unlock()
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		<-locked
		_ = txr.InTx(context.Background(), func(ctx context.Context) error {
			if _, err := e.svc.LockChannelForPlacement(ctx, c.ID); err != nil {
				t.Error(err)
			}
			mu.Lock()
			secondAt = time.Now()
			mu.Unlock()
			return nil
		})
	}()
	wg.Wait()
	if secondAt.Before(committedAt) {
		t.Fatalf("second lock at %s before first transaction ended at %s", secondAt, committedAt)
	}
	if _, err := e.svc.LockChannelForPlacement(context.Background(), uuid.New()); !app.IsNotFound(err) {
		t.Fatalf("unknown channel: %v", err)
	}
}

func TestChannelsForPlacement(t *testing.T) {
	e := newEnv(t)
	visible := e.active(-80000000102)
	big := e.active(-80000000103)
	// D-11: канал от 10 000 подписчиков без подтверждённого номера РКН не виден.
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET subscribers_count = 12000 WHERE id = $1`, big.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.ChannelsForPlacement(context.Background(), []uuid.UUID{visible.ID, big.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("channels %d", len(got))
	}
	if !got[visible.ID].Visible || got[big.ID].Visible || got[big.ID].Channel.Status != domain.StatusActive {
		t.Fatalf("visibility: %+v %+v", got[visible.ID], got[big.ID])
	}
	if len(got[visible.ID].Channel.Prices) != 1 || got[visible.ID].Formats["f_1_24"].TopHours != 1 {
		t.Fatalf("prices and formats: %+v", got[visible.ID])
	}
}

func TestUnavailabilityAgainstPlacements(t *testing.T) {
	e := newEnv(t)
	c := e.moderated(-80000000104)
	org := *c.OrganizationID
	sch := &schedule{conflict: true, cart: map[uuid.UUID]bool{c.ID: true}}
	e.svc.SetPlacementPorts(e.avail, sch, sch)
	day := 24 * time.Hour
	iv := domain.Unavailability{StartsAt: start.Add(day), EndsAt: start.Add(2 * day)}
	if _, err := e.svc.AddUnavailability(e.ctx(), org, c.ID, iv); reason(t, err) != "validation_failed/placement_conflict" {
		t.Fatalf("interval over scheduled placement: %v", err)
	}
	if !containsLog(e, "unavailability conflicts with placement") || sch.calls != 1 {
		t.Fatal("conflict not logged")
	}
	sch.conflict = false
	if _, err := e.svc.AddUnavailability(e.ctx(), org, c.ID, iv); err != nil {
		t.Fatalf("free interval: %v", err)
	}
	list, err := e.svc.UnavailabilityBetween(context.Background(), c.ID, start.Add(36*time.Hour), start.Add(72*time.Hour))
	if err != nil || len(list) != 1 {
		t.Fatalf("between: %v %v", list, err)
	}
	if list, _ := e.svc.UnavailabilityBetween(context.Background(), c.ID, start.Add(3*day), start.Add(4*day)); len(list) != 0 {
		t.Fatalf("after interval: %v", list)
	}
	// in_cart каталога — через порт корзины; без организации — пусто.
	in := e.svc.InCart(context.Background(), &org, []uuid.UUID{c.ID, uuid.New()})
	if !in[c.ID] || len(in) != 1 {
		t.Fatalf("in cart %v", in)
	}
	if len(e.svc.InCart(context.Background(), nil, []uuid.UUID{c.ID})) != 0 {
		t.Fatal("in cart without organization")
	}
}
