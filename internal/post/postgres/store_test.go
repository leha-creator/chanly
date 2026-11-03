package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*postgres.Store, *dbtest.DB) {
	d := dbtest.New(t)
	return postgres.New(d.Pool, logx.Discard()), d
}

func mediaFile(org, user uuid.UUID, createdAt time.Time) domain.MediaFile {
	id := ids.New()
	return domain.MediaFile{ID: id, OrganizationID: org, StorageKey: "media/" + id.String() + ".jpg", Mime: domain.MimeJPEG,
		SizeBytes: 1000, Width: 800, Height: 600, SHA256: id.String(), OriginalName: "a.jpg", CreatedBy: user, CreatedAt: createdAt}
}

func campaignWithPost(t *testing.T, st *postgres.Store, org, user uuid.UUID, createdAt time.Time) (domain.Campaign, domain.Post) {
	t.Helper()
	ctx := context.Background()
	c, err := domain.NewCampaign(ids.New(), org, user, domain.CampaignInput{Name: ptr("Кампания")}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Campaigns().Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	p := domain.NewPost(ids.New(), org, c.ID, createdAt)
	if err := st.Posts().Insert(ctx, p); err != nil {
		t.Fatal(err)
	}
	return c, p
}

func ptr[T any](v T) *T { return &v }

func TestMediaFilesListCursor(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org, user := posttest.Advertiser(t, d.Pool)
	other, otherUser := posttest.Advertiser(t, d.Pool)

	// 7 файлов, у пяти одинаковый created_at: курсор должен обойти каждый ровно по разу.
	var want []uuid.UUID
	for i := range 7 {
		at := now
		if i >= 5 {
			at = now.Add(time.Duration(i) * time.Minute)
		}
		f := mediaFile(org, user, at)
		if err := st.MediaFiles().Insert(ctx, f); err != nil {
			t.Fatal(err)
		}
		want = append(want, f.ID)
	}
	if err := st.MediaFiles().Insert(ctx, mediaFile(other, otherUser, now)); err != nil {
		t.Fatal(err)
	}
	deleted := mediaFile(org, user, now)
	if err := st.MediaFiles().Insert(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	if err := st.MediaFiles().SoftDelete(ctx, org, deleted.ID, now); err != nil {
		t.Fatal(err)
	}

	var got []uuid.UUID
	var after *domain.PageCursor
	for {
		page, err := st.MediaFiles().List(ctx, org, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(page); i++ {
			if page[i].CreatedAt.After(page[i-1].CreatedAt) {
				t.Fatal("not newest first")
			}
		}
		for _, f := range page {
			got = append(got, f.ID)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].String() < want[j].String() })
	sort.Slice(got, func(i, j int) bool { return got[i].String() < got[j].String() })
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("listed %d files, want %d (deleted and foreign files must be hidden)", len(got), len(want))
	}

	// Удалённый не находится ни по идентификатору, ни по sha256; повторное удаление — ErrNotFound.
	if _, err := st.MediaFiles().Get(ctx, org, deleted.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if _, ok, err := st.MediaFiles().FindBySHA256(ctx, org, deleted.SHA256); ok || err != nil {
		t.Fatalf("find deleted by sha: %v %v", ok, err)
	}
	if err := st.MediaFiles().SoftDelete(ctx, org, deleted.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if any, err := st.MediaFiles().GetAny(ctx, deleted.ID); err != nil || any.DeletedAt == nil {
		t.Fatalf("get any: %+v %v", any, err)
	}
	live, err := st.MediaFiles().LiveSubset(ctx, org, []uuid.UUID{want[0], deleted.ID, ids.New()})
	if err != nil || len(live) != 1 || live[0].ID != want[0] {
		t.Fatalf("live subset: %v %v", live, err)
	}
	if _, err := st.MediaFiles().Get(ctx, other, want[0]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign file: %v", err)
	}
}

func TestRemoveMediaFromDrafts(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org, user := posttest.Advertiser(t, d.Pool)
	other, otherUser := posttest.Advertiser(t, d.Pool)
	a, b := ids.New(), ids.New()

	save := func(org, user uuid.UUID, media []uuid.UUID) domain.Post {
		_, p := campaignWithPost(t, st, org, user, now)
		if err := p.SaveDraft(domain.DraftInput{Doc: domain.Doc{Blocks: []domain.Block{{Type: domain.BlockParagraph,
			Content: []domain.Text{{Text: "Текст"}}}}}, MediaIDs: media}, 10, now); err != nil {
			t.Fatal(err)
		}
		if err := st.Posts().SaveDraft(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := save(org, user, []uuid.UUID{a, b})
	p2 := save(org, user, []uuid.UUID{b, a})
	foreign := save(other, otherUser, []uuid.UUID{a})

	n, err := st.Posts().RemoveMediaFromDrafts(ctx, org, a, now.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("removed from %d drafts, err %v", n, err)
	}
	for _, p := range []domain.Post{p1, p2} {
		got, err := st.Posts().GetByCampaign(ctx, org, p.CampaignID)
		if err != nil || len(got.Draft.MediaIDs) != 1 || got.Draft.MediaIDs[0] != b || !got.UpdatedAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("draft after removal: %+v %v", got.Draft.MediaIDs, err)
		}
		if got.Draft.Doc.Blocks[0].Content[0].Text != "Текст" || got.Draft.TokenCarrier != domain.CarrierMedia {
			t.Fatalf("draft content lost: %+v", got.Draft)
		}
	}
	got, err := st.Posts().GetByCampaign(ctx, other, foreign.CampaignID)
	if err != nil || len(got.Draft.MediaIDs) != 1 {
		t.Fatalf("foreign draft changed: %+v %v", got.Draft.MediaIDs, err)
	}
	if _, err := st.Posts().GetByCampaign(ctx, other, p1.CampaignID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign post: %v", err)
	}
}

func TestDerivativeEnsureIdempotent(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org, user := posttest.Advertiser(t, d.Pool)
	f := mediaFile(org, user, now)
	if err := st.MediaFiles().Insert(ctx, f); err != nil {
		t.Fatal(err)
	}
	d1, pending, err := st.Derivatives().Ensure(ctx, f.ID, domain.KindLabeled, "h1", now)
	if err != nil || !pending {
		t.Fatalf("ensure: %v %v", pending, err)
	}
	d2, _, err := st.Derivatives().Ensure(ctx, f.ID, domain.KindLabeled, "h1", now.Add(time.Hour))
	if err != nil || d2.ID != d1.ID || !d2.CreatedAt.Equal(now) {
		t.Fatalf("second ensure: %+v %v", d2, err)
	}
	d1.Status, d1.StorageKey, d1.Mime, d1.Width, d1.Height, d1.SizeBytes = domain.DerivativeReady, "derivative/x.jpg", domain.MimeJPEG, 800, 600, 500
	if ok, err := st.Derivatives().MarkReady(ctx, d1); !ok || err != nil {
		t.Fatalf("mark ready: %v %v", ok, err)
	}
	if ok, err := st.Derivatives().MarkReady(ctx, d1); ok || err != nil {
		t.Fatalf("mark ready twice: %v %v", ok, err)
	}
	if got, pending, err := st.Derivatives().Ensure(ctx, f.ID, domain.KindLabeled, "h1", now); err != nil || pending || !got.Ready() {
		t.Fatalf("ensure ready: %+v %v %v", got, pending, err)
	}

	// Очищенная производная снова ждёт построения.
	if err := st.Derivatives().MarkPurged(ctx, d1.ID, now); err != nil {
		t.Fatal(err)
	}
	got, pending, err := st.Derivatives().Ensure(ctx, f.ID, domain.KindLabeled, "h1", now)
	if err != nil || !pending || got.PurgedAt != nil || got.StorageKey != "" {
		t.Fatalf("ensure after purge: %+v %v %v", got, pending, err)
	}
	list, err := st.Derivatives().ListByFiles(ctx, []uuid.UUID{f.ID}, domain.KindLabeled)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
}

func readyDerivative(t *testing.T, st *postgres.Store, fileID uuid.UUID, kind domain.DerivativeKind) domain.Derivative {
	t.Helper()
	ctx := context.Background()
	d, _, err := st.Derivatives().Ensure(ctx, fileID, kind, "h-"+string(kind), now)
	if err != nil {
		t.Fatal(err)
	}
	d.StorageKey, d.Mime, d.Width, d.Height, d.SizeBytes = "derivative/"+d.ID.String()+".jpg", domain.MimeJPEG, 800, 600, 500
	if _, err := st.Derivatives().MarkReady(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestVersionsNumbersAndImmutability(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	runner := tx.NewRunner(d.Pool, logx.Discard())
	org, user := posttest.Advertiser(t, d.Pool)
	f := mediaFile(org, user, now)
	if err := st.MediaFiles().Insert(ctx, f); err != nil {
		t.Fatal(err)
	}
	deriv := readyDerivative(t, st, f.ID, domain.KindLabeled)
	c, p := campaignWithPost(t, st, org, user, now)
	if err := p.SaveDraft(domain.DraftInput{Doc: domain.Doc{Blocks: []domain.Block{{Type: domain.BlockParagraph,
		Content: []domain.Text{{Text: "Текст"}}}}}, MediaIDs: []uuid.UUID{f.ID}}, 10, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Posts().SaveDraft(ctx, p); err != nil {
		t.Fatal(err)
	}

	// Два параллельных создания версии одного поста: блокировка поста не даёт одинаковых номеров.
	create := func() (int, error) {
		var number int
		err := runner.InTx(ctx, func(ctx context.Context) error {
			locked, err := st.Posts().GetByCampaignForUpdate(ctx, org, c.ID)
			if err != nil {
				return err
			}
			if number, err = st.Versions().NextNumber(ctx, locked.ID); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
			v, err := domain.NewVersion(ids.New(), locked, number, domain.Requisites{Name: posttest.Name, INN: "7707083893"}, "Текст",
				[]domain.VersionMedia{{Position: 0, MediaFileID: f.ID, DerivativeID: deriv.ID}}, user, now)
			if err != nil {
				return err
			}
			return st.Versions().Insert(ctx, v)
		})
		return number, err
	}
	var wg sync.WaitGroup
	numbers := make([]int, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); numbers[i], errs[i] = create() }()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || numbers[0] == numbers[1] {
		t.Fatalf("parallel versions: numbers %v, errors %v", numbers, errs)
	}

	versions, err := st.Versions().ListByPost(ctx, p.ID)
	if err != nil || len(versions) != 2 || versions[0].Number != 1 || versions[1].Number != 2 {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	v := versions[0]
	if len(v.Media) != 1 || v.Media[0].DerivativeID != deriv.ID || v.ContentDoc.Blocks[0].Content[0].Text != "Текст" ||
		v.TokenCarrier != domain.CarrierMedia || v.AdvertiserName != posttest.Name {
		t.Fatalf("version read back: %+v", v)
	}

	// approve → ready сохраняются; чужая lock_version — ErrStaleVersion.
	if err := v.Approve(user, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Versions().UpdateStatus(ctx, v); err != nil {
		t.Fatal(err)
	}
	stale := versions[0]
	_ = stale.Approve(user, now)
	if err := st.Versions().UpdateStatus(ctx, stale); !errors.Is(err, domain.ErrStaleVersion) {
		t.Fatalf("stale update: %v", err)
	}
	if err := v.MarkReady("E1", domain.DefaultFinalTextLimit); err != nil {
		t.Fatal(err)
	}
	if err := st.Versions().UpdateStatus(ctx, v); err != nil {
		t.Fatal(err)
	}
	got, err := st.Versions().Get(ctx, v.ID)
	if err != nil || got.Status != domain.VersionReady || got.Erid != "E1" || got.FinalText == "" || got.ModeratedBy == nil {
		t.Fatalf("ready version: %+v %v", got, err)
	}

	// Версия в ready не меняется: отказ триггера приходит как invalid_transition.
	got.LockVersion++
	got.FinalText = "подмена"
	if err := st.Versions().UpdateStatus(ctx, got); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("update ready version: %v", err)
	}
}

func TestCampaignsRoundTripAndList(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org, user := posttest.Advertiser(t, d.Pool)
	from, to := domain.NewDate(2026, 10, 20), domain.NewDate(2026, 10, 25)
	tf, _ := domain.ParseTimeOfDay("10:00")
	tt, _ := domain.ParseTimeOfDay("20:30")
	c, err := domain.NewCampaign(ids.New(), org, user, domain.CampaignInput{Name: ptr("Осень"), DateFrom: &from, DateTo: &to,
		TimeFrom: &tf, TimeTo: &tt, ExtraRequirements: ptr("Без конкурентов")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Campaigns().Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := st.Campaigns().GetForUpdate(ctx, org, c.ID)
	if err != nil || !got.DateFrom.Equal(from) || !got.DateTo.Equal(to) || got.TimeFrom.String() != "10:00" ||
		got.TimeTo.String() != "20:30" || *got.ExtraRequirements != "Без конкурентов" || got.Status != domain.CampaignDraft {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if _, err := got.Update(domain.CampaignInput{Name: ptr("Осень 2")}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.Campaigns().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := domain.NewCampaign(ids.New(), org, user, domain.CampaignInput{Name: ptr("x")}, now); err != nil {
			t.Fatal(err)
		}
		cc, _ := domain.NewCampaign(ids.New(), org, user, domain.CampaignInput{Name: ptr(fmt.Sprint("К", i))}, now.Add(time.Duration(i+1)*time.Minute))
		if err := st.Campaigns().Insert(ctx, cc); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.Campaigns().List(ctx, org, nil, nil, 2)
	if err != nil || len(page) != 2 || page[0].Name != "К2" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	last := page[1]
	rest, err := st.Campaigns().List(ctx, org, nil, &domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}, 10)
	if err != nil || len(rest) != 2 || rest[1].Name != "Осень 2" {
		t.Fatalf("second page: %+v %v", rest, err)
	}
	ordered := domain.CampaignOrdered
	if none, err := st.Campaigns().List(ctx, org, &ordered, nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("status filter: %v %v", none, err)
	}
	other, _ := posttest.Advertiser(t, d.Pool)
	if _, err := st.Campaigns().Get(ctx, other, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign campaign: %v", err)
	}
}

func TestPurgeSelections(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org, user := posttest.Advertiser(t, d.Pool)
	old := now.AddDate(0, 0, -40)

	gone := mediaFile(org, user, old)
	kept := mediaFile(org, user, old) // удалён, но входит в версию
	live := mediaFile(org, user, old)
	for _, f := range []domain.MediaFile{gone, kept, live} {
		if err := st.MediaFiles().Insert(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	livePreview := readyDerivative(t, st, live.ID, domain.KindPreview)
	livePlain := readyDerivative(t, st, live.ID, domain.KindPlain)
	keptLabeled := readyDerivative(t, st, kept.ID, domain.KindLabeled)
	if _, err := d.Pool.Exec(ctx, `UPDATE media_derivative SET created_at = $1`, old); err != nil {
		t.Fatal(err)
	}
	_, p := campaignWithPost(t, st, org, user, now)
	p.Draft.MediaIDs = []uuid.UUID{kept.ID}
	v, err := domain.NewVersion(ids.New(), p, 1, domain.Requisites{Name: "a", INN: "1"}, "x",
		[]domain.VersionMedia{{Position: 0, MediaFileID: kept.ID, DerivativeID: keptLabeled.ID}}, user, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Versions().Insert(ctx, v); err != nil {
		t.Fatal(err)
	}
	for _, f := range []domain.MediaFile{gone, kept} {
		if err := st.MediaFiles().SoftDelete(ctx, org, f.ID, old); err != nil {
			t.Fatal(err)
		}
	}

	files, err := st.MediaFiles().ForPurge(ctx, now.AddDate(0, 0, -30), 100)
	if err != nil || len(files) != 1 || files[0].ID != gone.ID {
		t.Fatalf("files for purge: %+v %v", files, err)
	}
	derivs, err := st.Derivatives().ForPurge(ctx, now.AddDate(0, 0, -30), 100)
	if err != nil || len(derivs) != 1 || derivs[0].ID != livePlain.ID {
		t.Fatalf("derivatives for purge: %+v %v (live preview %s must stay)", derivs, err, livePreview.ID)
	}
	if err := st.MediaFiles().MarkPurged(ctx, gone.ID, now); err != nil {
		t.Fatal(err)
	}
	if files, _ := st.MediaFiles().ForPurge(ctx, now.AddDate(0, 0, -30), 100); len(files) != 0 {
		t.Fatalf("purged file selected again: %+v", files)
	}
}
