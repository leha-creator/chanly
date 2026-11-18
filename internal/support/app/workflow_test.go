package app_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/support/app"
	"github.com/leha-creator/chanly/internal/support/domain"
	supportpg "github.com/leha-creator/chanly/internal/support/postgres"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type provider struct {
	calls     int
	err       error
	uploadErr error
}

func (p *provider) Send(context.Context, string, string, []domain.Attachment) (string, error) {
	p.calls++
	if p.err != nil {
		return "", p.err
	}
	return fmt.Sprintf("mid.out.%d", p.calls), nil
}
func (p *provider) Upload(context.Context, string, string, io.Reader) (string, error) {
	return "file-token", p.uploadErr
}

func TestPermanentUploadRejectionUnblocksNextMessage(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	log := logx.Discard()
	clk := clock.NewManual(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC))
	actor := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO "user"(id,email,admin_role,status,created_at,updated_at) VALUES($1,'upload-test@example.test','moderator','active',$2,$2)`, actor, clk.Now()); err != nil {
		t.Fatal(err)
	}
	repo := supportpg.New(d.Pool, log)
	files := storagefake.New()
	p := &provider{uploadErr: &domain.DeliveryError{Reason: "upload_rejected_415", Definitive: true}}
	s := &app.Service{Repo: repo, Tx: tx.NewRunner(d.Pool, log), Jobs: job.NewQueue(d.Pool, clk, log), Clock: clk, Log: log, Storage: files, Provider: p, Enabled: true, Limits: domain.DefaultLimits()}
	if err := s.Ingest(ctx, domain.Incoming{Type: "message_created", BotID: "100", UserID: "200", ExternalID: "incoming", Text: "Question", At: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.List(ctx, domain.Filter{ActorID: actor, Limit: 20})
	if err != nil || len(rows) != 1 {
		t.Fatal("conversation missing")
	}
	c := rows[0]
	if err = files.PutPrivate(ctx, "support/voice-test", strings.NewReader("fixture"), 7, "audio/mpeg"); err != nil {
		t.Fatal(err)
	}
	a := domain.Attachment{ID: ids.New(), ActorID: &actor, Type: "audio", MIME: "audio/mpeg", Status: "ready", StorageKey: "support/voice-test", Size: 7, Name: "voice.mp3"}
	if err = repo.SaveAttachment(ctx, a); err != nil {
		t.Fatal(err)
	}
	voice, err := s.Reply(ctx, c.ID, actor, domain.Reply{Attachments: []uuid.UUID{a.ID}, ClientKey: ids.New(), ReplyToSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.Reply(ctx, c.ID, actor, domain.Reply{Text: "Following answer", ClientKey: ids.New(), ReplyToSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Send(ctx, text.ID, 0); err == nil {
		t.Fatal("following message bypassed pending voice")
	}
	if err = s.Send(ctx, voice.ID, 0); err != nil {
		t.Fatal(err)
	}
	voice, err = repo.Message(ctx, voice.ID, false)
	if err != nil || voice.Status != domain.Failed || voice.Reason != "upload_rejected_415" || p.calls != 0 {
		t.Fatal("permanent upload rejection stayed queued or created a message")
	}
	if err = s.Send(ctx, text.ID, 0); err != nil {
		t.Fatal(err)
	}
	text, err = repo.Message(ctx, text.ID, false)
	if err != nil || text.Status != domain.Sent || p.calls != 1 {
		t.Fatal("following text remained blocked")
	}
}
func (*provider) ResolveVideo(context.Context, string) (string, error) {
	return "", errors.New("not configured")
}
func TestPersistentConversationWorkflow(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	log := logx.Discard()
	clk := clock.NewManual(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC))
	repo := supportpg.New(d.Pool, log)
	actor := ids.New()
	_, e := d.Pool.Exec(ctx, `INSERT INTO "user"(id,email,admin_role,status,created_at,updated_at) VALUES($1,'operator@example.test','moderator','active',$2,$2)`, actor, clk.Now())
	if e != nil {
		t.Fatal(e)
	}
	p := &provider{}
	s := &app.Service{Repo: repo, Tx: tx.NewRunner(d.Pool, log), Jobs: job.NewQueue(d.Pool, clk, log), Clock: clk, Log: log, Storage: storagefake.New(), Provider: p, Enabled: true, Limits: domain.DefaultLimits()}
	incoming := domain.Incoming{Type: "message_created", BotID: "100", UserID: "9007199254740999", ExternalID: "mid.in", Text: "Нужна помощь", At: clk.Now()}
	for i := 0; i < 2; i++ {
		if e = s.Ingest(ctx, incoming); e != nil {
			t.Fatal(e)
		}
	}
	rows, n, e := s.List(ctx, domain.Filter{ActorID: actor, Limit: 20})
	if e != nil || n != 1 || len(rows) != 1 {
		t.Fatalf("list: %d %v", n, e)
	}
	c := rows[0]
	if c.Unread != 1 || !c.NeedsReply {
		t.Fatalf("inbox: %+v", c)
	}
	reply := domain.Reply{Text: "Проверяем", ClientKey: ids.New(), ReplyToSequence: c.LastSequence}
	m, e := s.Reply(ctx, c.ID, actor, reply)
	if e != nil {
		t.Fatal(e)
	}
	same, e := s.Reply(ctx, c.ID, actor, reply)
	if e != nil || same.ID != m.ID {
		t.Fatal("idempotent reply failed")
	}
	// A new incoming arrives while the operator is preparing the previous answer.
	incoming.ExternalID = "mid.in.2"
	incoming.Text = "Ещё один вопрос"
	if e = s.Ingest(ctx, incoming); e != nil {
		t.Fatal(e)
	}
	if e = s.Send(ctx, m.ID, 0); e != nil {
		t.Fatal(e)
	}
	if e = s.Send(ctx, m.ID, 0); e != nil {
		t.Fatal(e)
	}
	if p.calls != 1 {
		t.Fatal("duplicate provider send")
	}
	c, e = s.Conversation(ctx, c.ID, actor)
	if e != nil || !c.NeedsReply {
		t.Fatal("new incoming incorrectly marked answered")
	}
	if e = s.Read(ctx, c.ID, actor, 1); e != nil {
		t.Fatal(e)
	}
	if e = s.Read(ctx, c.ID, actor, 0); e != nil {
		t.Fatal(e)
	}
	c, e = s.Conversation(ctx, c.ID, actor)
	if e != nil || c.Unread != 1 {
		t.Fatal("read marker regressed")
	}
	var count int
	if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM support_message WHERE conversation_id=$1`, c.ID).Scan(&count); e != nil || count != 3 {
		t.Fatalf("duplicate messages: %d %v", count, e)
	}
	var events int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM realtime_event WHERE topic='admin.support.changed'`).Scan(&events)
	if events == 0 {
		t.Fatal("support mutations did not emit transactional SSE")
	}

	// Simultaneous HTTP retries serialize on the conversation, producing one job.
	reply.ClientKey = ids.New()
	var wg sync.WaitGroup
	results := make(chan domain.Message, 8)
	errorsFound := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := s.Reply(ctx, c.ID, actor, reply)
			results <- m
			errorsFound <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var concurrent domain.Message
	for got := range results {
		if concurrent.Sequence != 0 && concurrent.ID != got.ID {
			t.Fatal("concurrent idempotency failed")
		}
		concurrent = got
	}
	if _, e = d.Pool.Exec(ctx, `UPDATE support_message SET status='sending' WHERE id=$1`, concurrent.ID); e != nil {
		t.Fatal(e)
	}
	if e = repo.Recover(ctx, clk.Now().Add(16*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.Send(ctx, concurrent.ID, 0); e != nil || p.calls != 1 {
		t.Fatal("interrupted send was replayed")
	}

	// An edit delivered before the original must not be lost.
	second := domain.Incoming{Type: "message_edited", BotID: "100", UserID: "200", ExternalID: "mid.second", Text: "Исправлено", At: clk.Now().Add(time.Second)}
	if e = s.Ingest(ctx, second); e != nil {
		t.Fatal(e)
	}
	second.Type, second.Text, second.At = "message_created", "Исходное", clk.Now()
	if e = s.Ingest(ctx, second); e != nil {
		t.Fatal(e)
	}
	rows, _, e = s.List(ctx, domain.Filter{ActorID: actor, Query: "200", Limit: 20})
	if e != nil || len(rows) != 1 {
		t.Fatal("second conversation missing")
	}
	c2 := rows[0]
	history, e := s.Messages(ctx, c2.ID, actor, 0, 20)
	if e != nil || len(history) != 1 || history[0].Text != "Исправлено" || !history[0].Edited {
		t.Fatal("pending edit was lost")
	}

	// Failure is visible before the old running job has been acknowledged.
	failed, e := s.Reply(ctx, c2.ID, actor, domain.Reply{Text: "Ответ", ClientKey: ids.New(), ReplyToSequence: 1})
	if e != nil {
		t.Fatal(e)
	}
	p.err = &domain.DeliveryError{Reason: "recipient_unavailable", Definitive: true}
	if e = s.Send(ctx, failed.ID, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = d.Pool.Exec(ctx, `UPDATE job SET status='running' WHERE dedupe_key=$1`, "support_send:"+failed.ID.String()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Retry(ctx, failed.ID); e != nil {
		t.Fatal(e)
	}
	if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM job WHERE kind=$1 AND payload->>'id'=$2 AND status='pending'`, app.SendKind, failed.ID.String()).Scan(&count); e != nil || count != 1 {
		t.Fatal("operator retry swallowed by running job")
	}
	p.err = nil
	if e = s.Send(ctx, failed.ID, 0); e != nil {
		t.Fatal(e)
	}

	// Retire database rows first, and retain a durable blob deletion on failure.
	files := s.Storage.(*storagefake.Storage)
	key := "support/test-retired"
	if e = files.PutPrivate(ctx, key, strings.NewReader("fixture"), 7, "application/octet-stream"); e != nil {
		t.Fatal(e)
	}
	if e = repo.SaveAttachment(ctx, domain.Attachment{ID: ids.New(), MessageID: &failed.ID, Type: "file", Status: "ready", StorageKey: key}); e != nil {
		t.Fatal(e)
	}
	if _, e = d.Pool.Exec(ctx, `UPDATE support_conversation SET status='closed',updated_at=$1 WHERE id IN ($2,$3)`, clk.Now().Add(-200*24*time.Hour), c.ID, c2.ID); e != nil {
		t.Fatal(e)
	}
	files.FailWith(storagefake.OpDelete, errors.New("temporary storage failure"))
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM support_conversation WHERE id=$1`, c.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("unknown send history was retired")
	}
	if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM support_conversation WHERE id=$1`, c2.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired history was not retired")
	}
	if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM support_blob_gc WHERE storage_key=$1`, key).Scan(&count); e != nil || count != 1 {
		t.Fatal("blob deletion was lost")
	}
	files.FailWith(storagefake.OpDelete, nil)
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	if _, _, e = files.Get(ctx, key); !errors.Is(e, storage.ErrNotFound) {
		t.Fatal("retired blob still exists")
	}
}
