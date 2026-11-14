package app_test

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/post/app"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func albumFiles(e *env) []uuid.UUID {
	e.t.Helper()
	var files []uuid.UUID
	for i := range 3 {
		files = append(files, e.upload(imagingtest.OpaquePNG(640+i, 480)).File.ID)
	}
	e.drain()
	return files
}

func saveAlbum(e *env, campaign uuid.UUID, files []uuid.UUID) {
	e.t.Helper()
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, campaign, draftInput(doc("Тестовый альбом"), files...)); err != nil {
		e.t.Fatal(err)
	}
}

func assertAlbum(e *env, campaign uuid.UUID, files []uuid.UUID) domain.Version {
	e.t.Helper()
	pv, err := e.svc.Preview(e.ctx(), e.org, campaign)
	if err != nil || len(pv.Problems) != 0 || len(pv.MediaURLs) != len(files) {
		e.t.Fatalf("preview %+v %v", pv, err)
	}
	v, err := e.svc.CreateVersion(e.ctx(), e.org, campaign, e.user, 1001)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, m := range v.Media {
		d, err := e.store.Derivatives().Get(e.ctx(), m.DerivativeID)
		want := domain.NewDerivativeParams(domain.NeededKindAt(v.TokenCarrier, i), posttest.Name, "7707083893")
		if err != nil || m.Position != i || m.MediaFileID != files[i] || d.Kind != want.Kind || d.ParamsHash != want.Hash() {
			e.t.Fatalf("position %d: %+v %+v %v", i, m, d, err)
		}
		u, err := url.Parse(pv.MediaURLs[i])
		if err != nil {
			e.t.Fatal(err)
		}
		key := strings.TrimPrefix(u.Path, "/")
		object, ok := e.files.Object(key)
		if !ok {
			e.t.Fatalf("preview object not found: %s", key)
		}
		r, err := e.svc.OpenDerivative(e.ctx(), d.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(data, object.Data) {
			e.t.Fatal("preview and snapshot bytes differ")
		}
	}
	e.drain()
	creative, err := e.svc.CreativeContent(e.ctx(), v.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	publication, err := e.svc.PublicationContent(e.ctx(), v.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, m := range v.Media {
		d, _ := e.store.Derivatives().Get(e.ctx(), m.DerivativeID)
		public, ok := e.files.Public(d.PublicStorageKey)
		private, _ := e.files.Object(d.StorageKey)
		if !ok || !bytes.Equal(public.Data, private.Data) || creative.ImageURLs[i] != d.PublicURL || publication.Media[i].DerivativeID != d.ID {
			e.t.Fatal("creative/publication media differ")
		}
	}
	return v
}

func TestAlbumReorderDeleteAndSnapshot(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	c := e.campaign("Альбом")
	// First prepare B as a first image. Its old ready labeled must not replace
	// the pending plain selected for its new position after A is inserted.
	saveAlbum(e, c.ID, files[1:])
	e.drain()
	saveAlbum(e, c.ID, files)
	pv, err := e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || len(pv.MediaURLs) != 0 || !slices.Contains(codes(pv.Problems), "media_processing") {
		t.Fatalf("partial preview: %+v %v", pv, err)
	}
	_, err = e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1001)
	wantReason(t, err, apperr.CodeConflict, "post_not_ready")
	if e.count(`SELECT count(*) FROM post_version`) != 0 {
		t.Fatal("partial snapshot created")
	}
	e.drain()
	old := assertAlbum(e, c.ID, files)
	before, _ := e.svc.GetVersion(e.ctx(), old.ID)
	reordered := []uuid.UUID{files[1], files[0], files[2]}
	saveAlbum(e, c.ID, reordered)
	e.drain()
	assertAlbum(e, c.ID, reordered)
	saveAlbum(e, c.ID, reordered[1:])
	e.drain()
	assertAlbum(e, c.ID, reordered[1:])
	// Delete from the library affects multiple drafts and queues the new first
	// without relying on Preview to repair the queue.
	c2 := e.campaign("Второй альбом")
	saveAlbum(e, c2.ID, reordered[1:])
	e.drain()
	if err := e.svc.Delete(e.ctx(), e.org, e.user, files[0]); err != nil {
		t.Fatal(err)
	}
	params := domain.NewDerivativeParams(domain.KindLabeled, posttest.Name, "7707083893")
	if _, ok, err := e.store.Derivatives().Find(e.ctx(), files[2], domain.KindLabeled, params.Hash()); err != nil || !ok {
		t.Fatalf("new first was not queued: %v", err)
	}
	e.drain()
	assertAlbum(e, c.ID, files[2:])
	assertAlbum(e, c2.ID, files[2:])
	after, _ := e.svc.GetVersion(e.ctx(), old.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reorder/delete changed a snapshot")
	}
	e.noSecrets(posttest.Name, "7707083893", "Тестовый альбом", "https://storage.test/")
}

func TestAlbumTextAndPendingPlain(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	c := e.campaign("Альбом")
	saveAlbum(e, c.ID, files)
	e.drain()
	// Purge only an unreferenced selected plain; a ready labeled of that file
	// must not make the album ready.
	plain := domain.NewDerivativeParams(domain.KindPlain, "", "")
	d, ok, err := e.store.Derivatives().Find(e.ctx(), files[2], domain.KindPlain, plain.Hash())
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = e.store.Derivatives().MarkPurged(e.ctx(), d.ID, e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	pv, err := e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || len(pv.MediaURLs) != 0 || !slices.Contains(codes(pv.Problems), "media_processing") {
		t.Fatalf("missing plain: %+v %v", pv, err)
	}
	e.drain()
	assertAlbum(e, c.ID, files)
	in := draftInput(doc("Текстовая пометка"), files...)
	carrier := domain.CarrierText
	in.TokenCarrier = &carrier
	if _, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, in); err != nil {
		t.Fatal(err)
	}
	e.drain()
	v := assertAlbum(e, c.ID, files)
	if v.TokenCarrier != domain.CarrierText {
		t.Fatal("carrier changed")
	}
}

func TestLegacyPlateSnapshotsStayImmutable(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	c := e.campaign("Старые версии")
	saveAlbum(e, c.ID, files)
	p, err := e.store.Posts().GetByCampaign(e.ctx(), e.org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := os.ReadFile("../imaging/testdata/labeled_long.png")
	if err != nil {
		t.Fatal(err)
	}
	media := []domain.VersionMedia{}
	for i, file := range files {
		hash := domain.ParamsHash(domain.KindLabeled, 1, posttest.Name, "7707083893")
		d, _, err := e.store.Derivatives().Ensure(e.ctx(), file, domain.KindLabeled, hash, e.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		d.StorageKey = "derivative/legacy-" + d.ID.String() + ".png"
		d.Mime = domain.MimePNG
		d.Width = 640
		d.Height = 480
		d.SizeBytes = int64(len(legacyBytes))
		if err = e.files.PutPrivate(e.ctx(), d.StorageKey, bytes.NewReader(legacyBytes), d.SizeBytes, d.Mime); err != nil {
			t.Fatal(err)
		}
		if _, err = e.store.Derivatives().MarkReady(e.ctx(), d); err != nil {
			t.Fatal(err)
		}
		if err = e.svc.PublishDerivative(e.ctx(), d.ID); err != nil {
			t.Fatal(err)
		}
		media = append(media, domain.VersionMedia{Position: i, MediaFileID: file, DerivativeID: d.ID})
	}
	snapshots := []app.VersionView{}
	for i, status := range []domain.VersionStatus{domain.VersionPendingModeration, domain.VersionApproved, domain.VersionReady} {
		v, err := domain.NewVersion(ids.New(), p, i+1, domain.Requisites{Name: posttest.Name, INN: "7707083893"}, domain.Serialize(p.Draft.Doc, nil), media, e.user, e.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err = e.store.Versions().Insert(e.ctx(), v); err != nil {
			t.Fatal(err)
		}
		if status != domain.VersionPendingModeration {
			if _, err = e.svc.ApproveVersion(e.ctx(), v.ID, e.user); err != nil {
				t.Fatal(err)
			}
		}
		if status == domain.VersionReady {
			if _, err = e.svc.MarkVersionReady(e.ctx(), v.ID, "legacy-erid"); err != nil {
				t.Fatal(err)
			}
		}
		view, err := e.svc.GetVersion(e.ctx(), v.ID)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, view)
	}
	e.req.set(e.org, "Новое название", "123456789012")
	saveAlbum(e, c.ID, []uuid.UUID{files[1], files[2]})
	e.drain()
	if err = e.svc.Delete(e.ctx(), e.org, e.user, files[0]); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(365 * 24 * time.Hour)
	if err = e.svc.PurgeMedia(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, before := range snapshots {
		after, err := e.svc.GetVersion(e.ctx(), before.Version.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("legacy %s mutated: %v", before.Version.Status, err)
		}
		for _, m := range before.Version.Media {
			d, _ := e.store.Derivatives().Get(e.ctx(), m.DerivativeID)
			private, ok := e.files.Object(d.StorageKey)
			public, pok := e.files.Public(d.PublicStorageKey)
			if !ok || !pok || !bytes.Equal(private.Data, legacyBytes) || !bytes.Equal(public.Data, legacyBytes) {
				t.Fatal("legacy bytes purged or replaced")
			}
		}
	}
}
