package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var seq atomic.Int64

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

type fx struct {
	t *testing.T
	d *dbtest.DB
	s *Store
}

func newFx(t *testing.T) *fx {
	d := dbtest.New(t)
	return &fx{t: t, d: d, s: New(d.Pool, logx.Discard())}
}

func (f *fx) exec(stmt string, args ...any) {
	f.t.Helper()
	if _, err := f.d.Pool.Exec(context.Background(), stmt, args...); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
}

func (f *fx) org() uuid.UUID {
	id := ids.New()
	f.exec(`INSERT INTO organization (id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, 'self_employed', $2, 'Организация', 'Контакт', '+79990000000', 'active', now(), now())`,
		id, fmt.Sprintf("77%010d", seq.Add(1)))
	return id
}

func newOrg(orgID uuid.UUID, st domain.SyncState, at time.Time) domain.Organization {
	return domain.Organization{ID: ids.New(), OrganizationID: orgID, Sync: st, CreatedAt: at, UpdatedAt: at}
}

func TestConcurrentInsertKeepsOneRecord(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	org := f.org()
	repo := f.s.Organizations()
	var wg sync.WaitGroup
	var created atomic.Int32
	got := make([]domain.Organization, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, ok, err := repo.Insert(ctx, newOrg(org, domain.NewSyncState(), t0))
			if err != nil {
				t.Errorf("insert: %v", err)
				return
			}
			if ok {
				created.Add(1)
			}
			got[i] = rec
		}()
	}
	wg.Wait()
	if created.Load() != 1 || got[0].ID != got[1].ID {
		t.Fatalf("created %d, ids %s %s", created.Load(), got[0].ID, got[1].ID)
	}

	// Сохранение и чтение полей синхронизации.
	rec := got[0]
	rec.IsRr = true
	rec.Sync.MarkSent(org.String(), "req-1", domain.OrdSuccess, "hash", t0)
	rec.UpdatedAt = t0
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	back, err := repo.GetForUpdate(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.IsRr || back.Sync.Status != domain.SyncSent || back.Sync.RequestID != "req-1" || !back.Sync.SentAt.Equal(t0) ||
		back.Sync.Attempts != 1 || !back.Sync.Ready() || back.Sync.ExternalID != org.String() {
		t.Fatalf("round trip: %+v", back)
	}
	if _, err := repo.ByOrganization(ctx, ids.New()); err != domain.ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestContractDateAndCreativeDelete(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	adv, plat := f.org(), f.org()
	date := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	ct := domain.Contract{ID: ids.New(), Kind: domain.ContractInitial, ClientOrganizationID: adv,
		ContractorOrganizationID: plat, SignedDate: date, Number: "AG-1", Sync: domain.NewSyncState(), CreatedAt: t0, UpdatedAt: t0}
	if _, created, err := f.s.Contracts().Insert(ctx, ct); err != nil || !created {
		t.Fatalf("insert contract: %v %v", created, err)
	}
	back, err := f.s.Contracts().ByParties(ctx, domain.ContractInitial, adv, plat)
	if err != nil || !back.SignedDate.Equal(date) || back.Number != "AG-1" {
		t.Fatalf("contract: %+v %v", back, err)
	}

	version := f.version(adv)
	cr := domain.NewCreative(ids.New(), version, t0)
	if _, _, err := f.s.Creatives().Insert(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Creatives().Delete(ctx, cr.ID); err != nil {
		t.Fatalf("delete unsent creative: %v", err)
	}
	cr = domain.NewCreative(ids.New(), version, t0)
	cr.ContractID = ct.ID
	cr.Sync.MarkSent(cr.ID.String(), "r", domain.OrdRequestAccepted, "h", t0)
	if _, _, err := f.s.Creatives().Insert(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Creatives().Delete(ctx, cr.ID); err != domain.ErrNotFound {
		t.Fatalf("sent creative must not be deleted: %v", err)
	}
}

func TestOverview(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	repo := f.s.Organizations()
	failed := domain.SyncState{Status: domain.SyncError, Error: "ИНН не найден", Attempts: 2}
	var errIDs []uuid.UUID
	for i := range 3 {
		o, _, err := repo.Insert(ctx, newOrg(f.org(), failed, t0.Add(time.Duration(i)*time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		errIDs = append(errIDs, o.ID)
	}
	plat := f.org()
	pl := domain.Platform{ID: ids.New(), ChannelID: f.channel(plat), OrganizationID: plat, Name: "Канал", URL: "https://max.ru/c",
		Sync: failed, CreatedAt: t0, UpdatedAt: t0.Add(30 * time.Second)}
	if _, _, err := f.s.Platforms().Insert(ctx, pl); err != nil {
		t.Fatal(err)
	}
	sent := domain.NewSyncState()
	sent.MarkSent("x", "r", domain.OrdRequestAccepted, "h", t0)
	if _, _, err := repo.Insert(ctx, newOrg(f.org(), sent, t0)); err != nil {
		t.Fatal(err)
	}

	// Курсор обходит все виды ровно по разу.
	ov := f.s.Overview()
	seen := map[uuid.UUID]int{}
	var cursor *domain.ErrorCursor
	for {
		page, err := ov.ListErrors(ctx, "", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, it := range page {
			seen[it.Ref.ID]++
			if it.Error != "ИНН не найден" || it.Attempts != 2 {
				t.Fatalf("item %+v", it)
			}
		}
		last := page[len(page)-1]
		cursor = &domain.ErrorCursor{UpdatedAt: last.UpdatedAt, ID: last.Ref.ID}
	}
	if len(seen) != 4 || seen[pl.ID] != 1 || seen[errIDs[2]] != 1 {
		t.Fatalf("error list seen %v", seen)
	}

	// Зависшие: порог 24 ч.
	stuck, err := ov.StuckSent(ctx, t0.Add(24*time.Hour), 10)
	if err != nil || len(stuck) != 1 || stuck[0].Ref.Type != domain.TypeOrganization {
		t.Fatalf("stuck after 24h: %+v %v", stuck, err)
	}
	if stuck, _ := ov.StuckSent(ctx, t0.Add(-time.Second), 10); len(stuck) != 0 {
		t.Fatalf("stuck before threshold: %+v", stuck)
	}

	counts, err := ov.CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[domain.TypeOrganization][domain.SyncError] != 3 || counts[domain.TypePlatform][domain.SyncError] != 1 ||
		counts[domain.TypeOrganization][domain.SyncSent] != 1 || counts[domain.TypeCreative][domain.SyncPending] != 0 {
		t.Fatalf("counts %v", counts)
	}
}

func (f *fx) channel(org uuid.UUID) uuid.UUID {
	id := ids.New()
	f.exec(`INSERT INTO channel (id, organization_id, max_chat_id, added_by_max_user_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, 7, 'moderation', now(), now())`, id, org, -seq.Add(1))
	return id
}

func (f *fx) version(org uuid.UUID) uuid.UUID {
	user, campaign, post, version := ids.New(), ids.New(), ids.New(), ids.New()
	f.exec(`INSERT INTO "user" (id, email, status, created_at, updated_at) VALUES ($1, $2, 'active', now(), now())`,
		user, user.String()+"@example.test")
	f.exec(`INSERT INTO campaign (id, organization_id, name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, 'Кампания', 'draft', $3, now(), now())`, campaign, org, user)
	f.exec(`INSERT INTO post (id, organization_id, campaign_id, status, draft, created_at, updated_at)
		VALUES ($1, $2, $3, 'draft', '{}', now(), now())`, post, org, campaign)
	f.exec(`INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier, token_position,
		advertiser_name, advertiser_inn, created_by, created_at)
		VALUES ($1, $2, 1, 'approved', '{}', 'текст', 'text', 'below', 'ООО', '7707083893', $3, now())`, version, post, user)
	return version
}

func TestMetrics(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	failed := domain.SyncState{Status: domain.SyncError, Error: "x"}
	for range 2 {
		if _, _, err := f.s.Organizations().Insert(ctx, newOrg(f.org(), failed, t0)); err != nil {
			t.Fatal(err)
		}
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(f.s))
	m := NewEridMetrics(reg)
	m.EridWait(90 * time.Second)
	if n := testutil.CollectAndCount(reg, "maxim_ord_objects"); n != len(domain.ObjectTypes)*len(domain.SyncStatuses) {
		t.Fatalf("ord object series %d", n)
	}
	if n := testutil.CollectAndCount(reg, "maxim_ord_erid_wait_seconds"); n != 1 {
		t.Fatalf("erid wait metric %d", n)
	}
}
