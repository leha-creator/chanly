package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/audit/sqlc"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestRecordInTransaction(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	l := audit.New(d.Pool, clk, logx.Discard())
	runner := tx.NewRunner(d.Pool, logx.Discard())
	ip := netip.MustParseAddr("203.0.113.7")
	ctx := audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, IP: &ip})
	entry := audit.Entry{Action: "test.action", ObjectType: "thing", ObjectID: "t-1",
		Data: audit.Diff(map[string]any{"inn": "7707083893", "phone": "+79990000001", "kpp": "770701001"},
			map[string]any{"inn": "7707083893", "phone": "+79990000002", "kpp": "770701001"}, []string{"phone"})}

	boom := errors.New("boom")
	err := runner.InTx(ctx, func(ctx context.Context) error {
		if err := l.Record(ctx, entry); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	q := sqlc.New(d.Pool)
	rows, err := q.ListAuditLogByObject(ctx, sqlc.ListAuditLogByObjectParams{ObjectType: "thing", ObjectID: "t-1"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rolled back entry exists: %v %v", rows, err)
	}

	if err := runner.InTx(ctx, func(ctx context.Context) error { return l.Record(ctx, entry) }); err != nil {
		t.Fatal(err)
	}
	rows, err = q.ListAuditLogByObject(ctx, sqlc.ListAuditLogByObjectParams{ObjectType: "thing", ObjectID: "t-1"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("entries: %v %v", rows, err)
	}
	r := rows[0]
	if r.ActorType != "advertiser" || r.Ip == nil || *r.Ip != ip || !r.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("row: %+v", r)
	}
	var data map[string]map[string]any
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["before"]["phone"] != "+7******0001" || data["after"]["phone"] != "+7******0002" {
		t.Fatalf("data: %v", data)
	}
	if strings.Contains(string(r.Data), "7707083893") || data["after"]["kpp"] != nil {
		t.Fatalf("unchanged or unmasked fields in data: %s", r.Data)
	}
}

func TestSystemActorByDefault(t *testing.T) {
	if a := audit.ActorFrom(context.Background()); a.Type != audit.ActorSystem {
		t.Fatalf("actor %+v", a)
	}
}

func TestMask(t *testing.T) {
	got := audit.Mask(map[string]any{"inn": "771234561234", "email": "ivan@example.ru", "kpp": "770701001", "cabinets": []string{"blogger"}})
	if got["inn"] != "77******1234" || got["email"] != "i***@example.ru" || got["kpp"] != "770701001" {
		t.Fatalf("mask: %v", got)
	}
}
