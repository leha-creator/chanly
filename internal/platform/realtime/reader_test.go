package realtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func testReader(t *testing.T, pool *pgxpool.Pool) (*Reader, *Hub) {
	t.Helper()
	h := NewHub(128)
	r := NewReader(pool, h, logx.Discard())
	if _, err := r.snapshot(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	h.setHealthy(true)
	return r, h
}

func poll(t *testing.T, r *Reader) {
	t.Helper()
	overflow, err := r.snapshot(t.Context(), false)
	if err != nil || overflow {
		t.Fatalf("poll overflow=%v err=%v", overflow, err)
	}
}
func take(t *testing.T, s *Subscription, topic string) Event {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case e := <-s.Events:
			if e.Topic == topic {
				return e
			}
		case <-s.Done:
			t.Fatal("stream closed")
		case <-deadline.C:
			t.Fatalf("missing %s", topic)
		}
	}
}

func createOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(t.Context(), `INSERT INTO organization(id,legal_type,inn,name,contact_name,phone,status,created_at,updated_at)
	VALUES($1,'self_employed',lpad((abs(hashtext($1::uuid::text))::bigint)::text,12,'0'),'test','test','test','active',now(),now())`, id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A separate pool represents worker/another API process. Both replicas see only committed writes.
func TestRollbackWorkerAndTwoReplicas(t *testing.T) {
	d := dbtest.New(t)
	org := createOrg(t, d.Pool)
	r1, h1 := testReader(t, d.Pool)
	r2, h2 := testReader(t, d.Pool)
	p := &httpx.Principal{UserID: uuid.New(), OrganizationID: &org}
	s1, _ := h1.Subscribe(p, false)
	s2, _ := h2.Subscribe(p, false)
	worker, err := pgxpool.New(t.Context(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	tx, err := worker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE organization SET name='rolled back' WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	poll(t, r1)
	poll(t, r2)
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	poll(t, r1)
	poll(t, r2)
	if len(s1.Events) != 0 || len(s2.Events) != 0 {
		t.Fatal("rollback/uncommitted write emitted invalidation")
	}
	if _, err = worker.Exec(t.Context(), `UPDATE organization SET name='worker commit' WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	poll(t, r1)
	poll(t, r2)
	for _, s := range []*Subscription{s1, s2} {
		e := take(t, s, "organization.changed")
		if e.ObjectID == nil || *e.ObjectID != org {
			t.Fatal(e)
		}
	}
}

func TestLateCommitBelowCursorAndHandshake(t *testing.T) {
	d := dbtest.New(t)
	org := createOrg(t, d.Pool)
	r, h := testReader(t, d.Pool)
	slow, err := d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback(t.Context())
	if _, err = slow.Exec(t.Context(), `SELECT realtime_emit('balance.changed','organization',$1)`, org); err != nil {
		t.Fatal(err)
	}
	// Subscribe after the write, before its commit: initial GET cannot see it yet.
	s, _ := h.Subscribe(&httpx.Principal{OrganizationID: &org}, false)
	if _, err = d.Pool.Exec(t.Context(), `SELECT realtime_emit('invoice.changed','organization',$1)`, org); err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	take(t, s, "invoice.changed")
	if err = slow.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	take(t, s, "balance.changed")
	poll(t, r)
	if len(s.Events) != 0 {
		t.Fatal("duplicate delivery after xmin advanced")
	}
}

func TestRealtimeMigrationDownAndUp(t *testing.T) {
	d := dbtest.New(t)
	if err := db.MigrateURL(t.Context(), d.OwnerURL, dbtest.AppRole, db.MigrateDown, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := d.Owner.QueryRow(t.Context(), `SELECT to_regclass('realtime_event') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("journal remains after down: %v %v", exists, err)
	}
	if err := db.MigrateURL(t.Context(), d.OwnerURL, dbtest.AppRole, db.MigrateUp, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	createOrg(t, d.Pool)
	var n int
	if err := d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM realtime_event`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("journal after reapply: %d %v", n, err)
	}
}

func TestReaderOverflowAndRetentionGap(t *testing.T) {
	d := dbtest.New(t)
	r, _ := testReader(t, d.Pool)
	_, err := d.Pool.Exec(t.Context(), `INSERT INTO realtime_event(scope,topic,object_id) SELECT 'catalog','channel.changed',gen_random_uuid() FROM generate_series(1,$1)`, batchLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if over, err := r.snapshot(t.Context(), false); err != nil || !over {
		t.Fatalf("overflow=%v err=%v", over, err)
	}
	if _, err = r.snapshot(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	_, err = d.Pool.Exec(t.Context(), `INSERT INTO realtime_epoch(singleton,generation,max_pruned_id) VALUES(true,1,$1)`, r.cursor+1)
	if err != nil {
		t.Fatal(err)
	}
	if over, err := r.snapshot(t.Context(), false); err != nil || !over {
		t.Fatalf("gap detection=%v %v", over, err)
	}
}

func TestSafeRetentionDoesNotDisconnectHealthyReaders(t *testing.T) {
	d := dbtest.New(t)
	org := createOrg(t, d.Pool)
	r, h := testReader(t, d.Pool)
	s, _ := h.Subscribe(&httpx.Principal{OrganizationID: &org}, false)
	if _, err := d.Pool.Exec(t.Context(), `SELECT realtime_emit('balance.changed','organization',$1)`, org); err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	take(t, s, "balance.changed")
	poll(t, r)
	if _, err := d.Pool.Exec(t.Context(), `WITH removed AS (DELETE FROM realtime_event RETURNING id,transaction_id)
		INSERT INTO realtime_epoch(singleton,generation,max_pruned_id,max_pruned_transaction)
		SELECT true,1,max(id),max(transaction_id::text::numeric)::text::xid8 FROM removed`); err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	select {
	case <-s.Done:
		t.Fatal("routine retention disconnected healthy reader")
	default:
	}
}

func TestOneCommittedOperationInvalidatesMultipleAreas(t *testing.T) {
	d := dbtest.New(t)
	org := createOrg(t, d.Pool)
	r, h := testReader(t, d.Pool)
	s, _ := h.Subscribe(&httpx.Principal{OrganizationID: &org}, false)
	tx, err := d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	_, err = tx.Exec(t.Context(), `INSERT INTO invoice(id,number,organization_id,amount,status,expires_at,payer_name,payer_inn,pdf_storage_key,created_at,updated_at,paid_at,paid_amount)
		VALUES($1,'MX-TEST',$2,100000,'paid',now()+interval '1 day','test','test','test',now(),now(),now(),100000)`, uuid.New(), org)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO ledger_account(id,kind,organization_id,balance,created_at,updated_at)
		VALUES($1,'advertiser_balance',$2,100000,now(),now())`, uuid.New(), org)
	if err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	if len(s.Events) != 0 {
		t.Fatal("operation visible before commit")
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	seen := map[string]bool{}
	for len(s.Events) > 0 {
		seen[(<-s.Events).Topic] = true
	}
	if !seen["invoice.changed"] || !seen["balance.changed"] {
		t.Fatalf("operation areas=%v", seen)
	}
}

func TestReaderFailureRecoveryAndShutdown(t *testing.T) {
	d := dbtest.New(t)
	h := NewHub(128)
	r := NewReader(d.Pool, h, logx.Discard())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	var sub *Subscription
	waitFor(t, func() bool { var err error; sub, err = h.Subscribe(&httpx.Principal{}, false); return err == nil })
	_, err := d.Owner.Exec(t.Context(), `ALTER TABLE realtime_event RENAME TO realtime_event_unavailable`)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Owner.Exec(context.Background(), `ALTER TABLE IF EXISTS realtime_event_unavailable RENAME TO realtime_event`)
	select {
	case <-sub.Done:
	case <-time.After(4 * time.Second):
		t.Fatal("read failure silently left stream healthy")
	}
	if _, err := h.Subscribe(&httpx.Principal{}, false); err == nil {
		t.Fatal("subscriber admitted during recovery")
	}
	_, err = d.Owner.Exec(t.Context(), `ALTER TABLE realtime_event_unavailable RENAME TO realtime_event`)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { var err error; sub, err = h.Subscribe(&httpx.Principal{}, false); return err == nil })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not stop")
	}
	select {
	case <-sub.Done:
	default:
		t.Fatal("shutdown did not close clients")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timeout")
}

// Real business writes produce distinct areas, not just notification events.
func TestPostRelationsNotificationsAndCatalogPrivacy(t *testing.T) {
	d := dbtest.New(t)
	org1, org2 := createOrg(t, d.Pool), createOrg(t, d.Pool)
	u1, u2, campaign, post := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{u1, u2} {
		if _, err := d.Pool.Exec(t.Context(), `INSERT INTO "user"(id,email,status,created_at,updated_at) VALUES($1,$1::uuid::text||'@example.test','active',now(),now())`, u); err != nil {
			t.Fatal(err)
		}
	}
	r, h := testReader(t, d.Pool)
	s1, _ := h.Subscribe(&httpx.Principal{UserID: u1, OrganizationID: &org1, Cabinets: []string{"advertiser"}}, false)
	s2, _ := h.Subscribe(&httpx.Principal{UserID: u2, OrganizationID: &org2, Cabinets: []string{"advertiser"}}, false)
	_, err := d.Pool.Exec(t.Context(), `INSERT INTO campaign(id,organization_id,name,status,created_by,created_at,updated_at) VALUES($1,$2,'test','draft',$3,now(),now());`, campaign, org1, u1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Pool.Exec(t.Context(), `INSERT INTO post(id,organization_id,campaign_id,status,draft,created_at,updated_at) VALUES($1,$2,$3,'draft','{}',now(),now())`, post, org1, campaign)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Pool.Exec(t.Context(), `INSERT INTO notification(id,user_id,organization_id,channel,template,status,created_at,updated_at) VALUES($1,$2,$3,'cabinet','test','sent',now(),now())`, uuid.New(), u1, org1)
	if err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	take(t, s1, "campaign.changed")
	e := take(t, s1, "post.changed")
	if e.CampaignID == nil || *e.CampaignID != campaign {
		t.Fatal("post missing campaign_id")
	}
	take(t, s1, "notification.changed")
	if len(s2.Events) > 0 {
		t.Fatal("cross-organization or personal notification leak")
	}
	_, err = d.Pool.Exec(t.Context(), `INSERT INTO channel(id,organization_id,max_chat_id,added_by_max_user_id,status,created_at,updated_at)
	VALUES($1,$2,100,100,'moderation',now(),now())`, uuid.New(), org1)
	if err != nil {
		t.Fatal(err)
	}
	poll(t, r)
	if len(s2.Events) > 0 {
		t.Fatal("private channel broadcast")
	}
}
