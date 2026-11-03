package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/notificationtest"
	"github.com/leha-creator/chanly/internal/notification/postgres"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func cabinet(user uuid.UUID, at time.Time, key string) domain.Notification {
	n := domain.Notification{ID: ids.New(), UserID: user, Channel: domain.ChannelCabinet, Template: "placement.request",
		Payload: map[string]any{"order_number": 1001}, Status: domain.StatusSent, SentAt: &at, CreatedAt: at, UpdatedAt: at}
	if key != "" {
		n.DedupeKey = &key
	}
	return n
}

func TestCabinetListReadAndCleanup(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	s := postgres.New(d.Pool, logx.Discard())
	user, _ := notificationtest.User(t, d.Pool, "")
	other, _ := notificationtest.User(t, d.Pool, "")

	var rows []domain.Notification
	for i := range 5 {
		rows = append(rows, cabinet(user, t0.Add(time.Duration(i)*time.Minute), ""))
	}
	rows = append(rows, cabinet(other, t0, ""))
	at := t0
	email := domain.Notification{ID: ids.New(), UserID: user, Channel: domain.ChannelEmail, Template: "placement.request",
		Payload: map[string]any{}, Status: domain.StatusPending, CreatedAt: at, UpdatedAt: at}
	rows = append(rows, email)
	if got, err := s.Insert(ctx, rows); err != nil || len(got) != len(rows) {
		t.Fatalf("insert: %v %d", err, len(got))
	}

	page, err := s.ListCabinet(ctx, user, false, nil, 3)
	if err != nil || len(page) != 3 || !page[0].CreatedAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("page 1: %v %+v", err, page)
	}
	if v, _ := page[0].Payload["order_number"].(interface{ String() string }); v == nil || v.String() != "1001" {
		t.Fatalf("payload number: %#v", page[0].Payload["order_number"])
	}
	last := page[2]
	page2, err := s.ListCabinet(ctx, user, false, &domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, 3)
	if err != nil || len(page2) != 2 {
		t.Fatalf("page 2: %v %d", err, len(page2))
	}
	if n, _ := s.CountCabinet(ctx, user, false); n != 5 {
		t.Fatalf("count %d", n)
	}

	if err := s.MarkRead(ctx, other, page[0].ID, t0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	if err := s.MarkRead(ctx, user, email.ID, t0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("email read: %v", err)
	}
	if err := s.MarkRead(ctx, user, page[0].ID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(ctx, user, page[0].ID, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("repeat read: %v", err)
	}
	if n, _ := s.CountCabinet(ctx, user, true); n != 4 {
		t.Fatalf("unread %d", n)
	}
	unread, _ := s.ListCabinet(ctx, user, true, nil, 10)
	if len(unread) != 4 {
		t.Fatalf("unread list %d", len(unread))
	}
	if n, err := s.MarkAllRead(ctx, user, t0.Add(3*time.Hour)); err != nil || n != 4 {
		t.Fatalf("read all: %v %d", err, n)
	}
	if n, _ := s.MarkAllRead(ctx, user, t0.Add(4*time.Hour)); n != 0 {
		t.Fatalf("read all again: %d", n)
	}

	// Очистка: pending не трогается.
	deleted, err := s.DeleteBefore(ctx, t0.Add(24*time.Hour), 100)
	if err != nil || deleted != 6 {
		t.Fatalf("cleanup: %v %d", err, deleted)
	}
	if _, err := s.GetForUpdate(ctx, email.ID); err != nil {
		t.Fatalf("pending kept: %v", err)
	}
}

func TestInsertSkipsRepeatedKeyAndConstraints(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	s := postgres.New(d.Pool, logx.Discard())
	user, _ := notificationtest.User(t, d.Pool, "")
	first := cabinet(user, t0, "reminder:p:T-01")
	if got, err := s.Insert(ctx, []domain.Notification{first}); err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	if got, err := s.Insert(ctx, []domain.Notification{cabinet(user, t0, "reminder:p:T-01")}); err != nil || len(got) != 0 {
		t.Fatalf("repeat: %v %d", err, len(got))
	}
	at := t0
	bad := domain.Notification{ID: ids.New(), UserID: user, Channel: domain.ChannelEmail, Template: "x", Payload: map[string]any{},
		Status: domain.StatusRead, ReadAt: &at, CreatedAt: at, UpdatedAt: at}
	if _, err := s.Insert(ctx, []domain.Notification{bad}); err == nil {
		t.Fatal("read email must violate constraint")
	}
	noMax := domain.Notification{ID: ids.New(), UserID: user, Channel: domain.ChannelMaxBot, Template: "x", Payload: map[string]any{},
		Status: domain.StatusPending, CreatedAt: at, UpdatedAt: at}
	if _, err := s.Insert(ctx, []domain.Notification{noMax}); err == nil {
		t.Fatal("max_bot without max_user_id must violate constraint")
	}

	n, err := s.GetForUpdate(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	n.MarkAttempt("timeout", t0)
	if err := s.Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetForUpdate(ctx, first.ID)
	if got.Attempts != 1 || got.LastError == nil || *got.LastError != "timeout" {
		t.Fatalf("update: %+v", got)
	}
}
