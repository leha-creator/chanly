package app_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/app"
	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/notificationtest"
	"github.com/leha-creator/chanly/internal/notification/postgres"
	"github.com/leha-creator/chanly/internal/notification/render"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type env struct {
	d          *dbtest.DB
	svc        *app.Service
	clk        *clock.Manual
	recipients *notificationtest.Recipients
	channels   notificationtest.Channels
	delivery   *notificationtest.Delivery
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	log := logx.Discard()
	clk := clock.NewManual(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	r, err := render.New("https://cabinet.test")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, clk: clk, recipients: notificationtest.NewRecipients(), channels: notificationtest.Channels{},
		delivery: &notificationtest.Delivery{}}
	e.svc = app.New(app.Deps{Tx: tx.NewRunner(d.Pool, log), Jobs: job.NewQueue(d.Pool, clk, log), Repo: postgres.New(d.Pool, log),
		Recipients: e.recipients, Channels: e.channels, Renderer: r, Messenger: e.delivery, Mailer: e.delivery, Clock: clk, Log: log})
	return e
}

// sendJobs — идентификаторы уведомлений в заданиях send_notification.
func sendJobs(t *testing.T, e *env) []uuid.UUID {
	t.Helper()
	rows, err := e.d.Owner.Query(context.Background(), `SELECT payload FROM job WHERE kind = 'send_notification' ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		id, err := app.DecodeSendPayload(raw)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func status(t *testing.T, e *env, id uuid.UUID) (string, int) {
	t.Helper()
	var st string
	var attempts int
	if err := e.d.Owner.QueryRow(context.Background(), `SELECT status, attempts FROM notification WHERE id = $1`, id).Scan(&st, &attempts); err != nil {
		t.Fatal(err)
	}
	return st, attempts
}

func blogger(t *testing.T, e *env) (org, user, channel uuid.UUID) {
	t.Helper()
	org, user, email := notificationtest.Organization(t, e.d.Pool)
	e.recipients.AddMember(org, user, email)
	channel = ids.New()
	e.channels[channel] = domain.ChannelInfo{Title: "Кухня", AddedByMaxUserID: 4242}
	return org, user, channel
}

func TestNotifyBloggerAndSend(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, user, channel := blogger(t, e)
	n := domain.Notice{Template: "placement.request", OrganizationID: org, Role: domain.RoleBlogger, ObjectType: "placement",
		ObjectID: ids.New().String(), Data: map[string]any{"channel_id": channel, "order_number": 1001}}
	if err := e.svc.Notify(ctx, n); err != nil {
		t.Fatal(err)
	}
	jobs := sendJobs(t, e)
	if len(jobs) != 2 {
		t.Fatalf("jobs %d, want max_bot and email", len(jobs))
	}
	page, err := e.svc.List(ctx, user, true, "", 10)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("cabinet: %v %+v", err, page)
	}
	if page.Items[0].Title != "Новый запрос на размещение в «Кухня»" || page.Items[0].Link() == "" {
		t.Fatalf("cabinet item: %+v", page.Items[0])
	}
	for _, id := range jobs {
		if err := e.svc.Send(ctx, id); err != nil {
			t.Fatal(err)
		}
		if st, _ := status(t, e, id); st != "sent" {
			t.Fatalf("status %s", st)
		}
		// Повтор задания после успеха ничего не отправляет.
		if err := e.svc.Send(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	msgs, mails := e.delivery.Messages(), e.delivery.Mails()
	if len(msgs) != 1 || msgs[0].MaxUser != 4242 || !strings.Contains(msgs[0].Text, "https://cabinet.test/blg/placements?placement=") {
		t.Fatalf("max messages: %+v", msgs)
	}
	if len(mails) != 1 || !strings.HasSuffix(mails[0].To, "@notify.test") || mails[0].Subject != page.Items[0].Title {
		t.Fatalf("mails: %+v", mails)
	}
}

func TestBotRejectedMailStillSent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, _, channel := blogger(t, e)
	e.delivery.FailMAX(job.Permanent(errors.New("max: chat.denied")))
	if err := e.svc.Notify(ctx, domain.Notice{Template: "channel.approved", OrganizationID: org, Role: domain.RoleBlogger,
		ObjectType: "channel", ObjectID: channel.String(), Data: map[string]any{"channel_id": channel.String()}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range sendJobs(t, e) {
		if err := e.svc.Send(ctx, id); err != nil {
			t.Fatalf("permanent failure must finish the job: %v", err)
		}
	}
	var failed, sent int
	_ = e.d.Owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'failed'), count(*) FILTER (WHERE status = 'sent' AND channel = 'email') FROM notification`).Scan(&failed, &sent)
	if failed != 1 || sent != 1 || len(e.delivery.Mails()) != 1 {
		t.Fatalf("failed %d, email sent %d", failed, sent)
	}
}

func TestTemporaryFailureRetriesThenExhausted(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, user, email := notificationtest.Organization(t, e.d.Pool)
	e.recipients.AddMember(org, user, email)
	if err := e.svc.Notify(ctx, domain.Notice{Template: "balance.deposited", OrganizationID: org, Role: domain.RoleAdvertiser,
		Data: map[string]any{"amount": int64(100000)}}); err != nil {
		t.Fatal(err)
	}
	ids := sendJobs(t, e)
	if len(ids) != 1 {
		t.Fatalf("jobs %d", len(ids))
	}
	e.delivery.FailMail(errors.New("smtp timeout"))
	if err := e.svc.Send(ctx, ids[0]); err == nil {
		t.Fatal("temporary failure must be retried")
	}
	if st, attempts := status(t, e, ids[0]); st != "pending" || attempts != 1 {
		t.Fatalf("after retry: %s %d", st, attempts)
	}
	e.svc.Exhausted(ctx, ids[0], errors.New("smtp timeout"))
	if st, attempts := status(t, e, ids[0]); st != "failed" || attempts != 1 {
		t.Fatalf("after exhausted: %s %d", st, attempts)
	}
}

func TestNoEmailFailsWithoutRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, user, email := notificationtest.Organization(t, e.d.Pool)
	e.recipients.AddMember(org, user, email)
	_ = e.svc.Notify(ctx, domain.Notice{Template: "closing_acts.ready", OrganizationID: org, Role: domain.RoleAdvertiser})
	e.recipients.SetEmail(user, "")
	id := sendJobs(t, e)[0]
	if err := e.svc.Send(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := status(t, e, id); st != "failed" {
		t.Fatalf("status %s", st)
	}
}

func TestNotifyRollsBackWithEvent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, user, email := notificationtest.Organization(t, e.d.Pool)
	e.recipients.AddMember(org, user, email)
	runner := tx.NewRunner(e.d.Pool, logx.Discard())
	boom := errors.New("event failed")
	err := runner.InTx(ctx, func(ctx context.Context) error {
		if err := e.svc.Notify(ctx, domain.Notice{Template: "placement.accepted", OrganizationID: org, Role: domain.RoleAdvertiser}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	_ = e.d.Owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification) + (SELECT count(*) FROM job WHERE kind = 'send_notification')`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows left after rollback: %d", n)
	}
}

func TestAdminCabinetAndReadAll(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin, mail := notificationtest.User(t, e.d.Pool, "senior")
	e.recipients.AddAdmin(admin, mail)
	for range 3 {
		if err := e.svc.Notify(ctx, domain.Notice{Template: "placement.failed", Role: domain.RoleAdmin, ObjectType: "placement",
			ObjectID: ids.New().String(), Data: map[string]any{"order_number": 1001}}); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(time.Minute)
	}
	if jobs := sendJobs(t, e); len(jobs) != 0 {
		t.Fatalf("admin gets cabinet only, jobs %d", len(jobs))
	}
	page, err := e.svc.List(ctx, admin, false, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Total != 3 {
		t.Fatalf("page: %v %+v", err, page)
	}
	if !strings.HasPrefix(page.Items[0].Link(), "/adm/placements/") {
		t.Fatalf("admin link %q", page.Items[0].Link())
	}
	if _, err := e.svc.List(ctx, admin, false, "broken", 2); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad cursor: %v", err)
	}
	other, _ := notificationtest.User(t, e.d.Pool, "")
	if err := e.svc.MarkRead(ctx, other, page.Items[0].ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	if err := e.svc.MarkRead(ctx, admin, page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.MarkAllRead(ctx, admin); err != nil || n != 2 {
		t.Fatalf("read all: %v %d", err, n)
	}
	if n, _ := e.svc.MarkAllRead(ctx, admin); n != 0 {
		t.Fatalf("read all again %d", n)
	}
}
