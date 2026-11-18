package postgres

import (
	"context"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/support/domain"
	"github.com/leha-creator/chanly/internal/support/postgres/sqlc"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestQueueRollbackAndMarkers(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(d.Pool, log)
	q := sqlc.New(d.Pool)
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	id, e := q.SupportCreateConversation(ctx, sqlc.SupportCreateConversationParams{ID: ids.New(), BotID: "support-test", MaxUserID: "9007199254740999", CreatedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	runner := tx.NewRunner(d.Pool, log)
	e = runner.InTx(ctx, func(ctx context.Context) error {
		_, _, e := s.QueueReply(ctx, id, ids.New(), domain.Reply{Text: "test", ClientKey: ids.New()}, now)
		return e
	})
	if e == nil {
		t.Fatal("nonexistent actor should violate FK")
	}
	c, e := s.Conversation(ctx, id, ids.New(), false)
	if e != nil || c.LastSequence != 0 {
		t.Fatalf("rollback failed: %v %+v", e, c)
	}
}
