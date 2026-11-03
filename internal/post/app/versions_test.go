package app_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

// readyDraft — кампания с черновиком: текст со ссылкой и одно изображение, производные построены.
func (e *env) readyDraft(carrier domain.TokenCarrier) domain.Campaign {
	e.t.Helper()
	c := e.campaign("К")
	img := e.upload(imagingtest.JPEG(800, 600, 0))
	in := draftInput(doc("Скидки https://shop.example.test/sale"), img.File.ID)
	in.TokenCarrier = &carrier
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, in); err != nil {
		e.t.Fatal(err)
	}
	e.drain()
	return c
}

func TestCreateVersion(t *testing.T) {
	for _, tt := range []struct {
		carrier domain.TokenCarrier
		kind    domain.DerivativeKind
	}{{domain.CarrierMedia, domain.KindLabeled}, {domain.CarrierText, domain.KindPlain}} {
		t.Run(string(tt.carrier), func(t *testing.T) {
			e := newEnv(t)
			c := e.readyDraft(tt.carrier)
			v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 42)
			if err != nil {
				t.Fatal(err)
			}
			if v.Number != 1 || v.Status != domain.VersionPendingModeration || v.AdvertiserName != posttest.Name ||
				v.AdvertiserINN != "7707083893" || !strings.Contains(v.Body, "utm_campaign=order_42") || len(v.Media) != 1 {
				t.Fatalf("%+v", v)
			}
			d, err := e.store.Derivatives().Get(context.Background(), v.Media[0].DerivativeID)
			if err != nil || d.Kind != tt.kind {
				t.Fatalf("version media derivative: %+v %v", d, err)
			}
			if n := e.count(`SELECT count(*) FROM job WHERE kind = 'publish_media_derivative'`); n != 1 {
				t.Fatalf("publish jobs %d", n)
			}

			// Публикация даёт public_url; повтор задания не копирует второй раз.
			e.drain()
			view, err := e.svc.GetVersion(e.ctx(), v.ID)
			if err != nil || len(view.MediaURLs) != 1 || !strings.HasPrefix(view.MediaURLs[0], storagefake.PublicBaseURL+"/pub/") {
				t.Fatalf("published: %+v %v", view.MediaURLs, err)
			}
			if err := e.svc.PublishDerivative(e.ctx(), d.ID); err != nil || e.files.Copies() != 1 {
				t.Fatalf("repeat publish: %v, copies %d", err, e.files.Copies())
			}

			// Вторая версия — номер 2; первая не меняется; правка черновика версии не трогает.
			if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Новый текст"))); err != nil {
				t.Fatal(err)
			}
			first, _ := e.svc.GetVersion(e.ctx(), v.ID)
			if first.Version.Body != v.Body || len(first.Version.Media) != 1 {
				t.Fatalf("draft edit changed version: %q", first.Version.Body)
			}
			v2, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 43)
			if err != nil || v2.Number != 2 || len(v2.Media) != 0 {
				t.Fatalf("second version: %+v %v", v2, err)
			}
		})
	}
}

func TestCreateVersionBlocked(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	if _, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("empty draft: %v", err)
	} else if ae, _ := apperr.As(err); ae.Details["reason"] != "post_not_ready" ||
		!slices.Contains(ae.Details["problems"].([]string), "empty_text") {
		t.Fatalf("details %v", ae.Details)
	}

	img := e.upload(imagingtest.JPEG(400, 300, 0))
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), img.File.ID)); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1)
	if ae, ok := apperr.As(err); !ok || !slices.Contains(ae.Details["problems"].([]string), "media_processing") {
		t.Fatalf("not built: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM post_version`); n != 0 {
		t.Fatalf("versions %d", n)
	}
}

func TestVersionTransitions(t *testing.T) {
	e := newEnv(t)
	c := e.readyDraft(domain.CarrierMedia)
	v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 42)
	if err != nil {
		t.Fatal(err)
	}
	admin := e.user
	if _, err := e.svc.MarkVersionReady(e.ctx(), v.ID, "E1"); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("ready before approve: %v", err)
	}
	if _, err := e.svc.ApproveVersion(e.ctx(), v.ID, admin); err != nil {
		t.Fatal(err)
	}
	ready, err := e.svc.MarkVersionReady(e.ctx(), v.ID, "2VtzqvAbc")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != domain.VersionReady || !strings.HasSuffix(ready.FinalText, "\n\nerid: 2VtzqvAbc") {
		t.Fatalf("final text %q", ready.FinalText)
	}
	if _, err := e.svc.RejectVersion(e.ctx(), v.ID, admin, "поздно"); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("reject after ready: %v", err)
	}

	// erid в ссылке: параметр у каждой ссылки, строки маркировки нет (пометка на плашке).
	position := domain.PositionLink
	in := draftInput(doc("Скидки https://shop.example.test/sale"))
	in.TokenPosition = &position
	carrier := domain.CarrierText
	in.TokenCarrier = &carrier
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, in); err != nil {
		t.Fatal(err)
	}
	v2, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 43)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApproveVersion(e.ctx(), v2.ID, admin); err != nil {
		t.Fatal(err)
	}
	ready2, err := e.svc.MarkVersionReady(e.ctx(), v2.ID, "E2")
	if err != nil || !strings.Contains(ready2.FinalText, "erid=E2") || !strings.HasSuffix(ready2.FinalText, "ИНН 7707083893.") {
		t.Fatalf("link erid: %q %v", ready2.FinalText, err)
	}

	v3, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 44)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RejectVersion(e.ctx(), v3.ID, admin, ""); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("empty reason: %v", err)
	}
	rejected, err := e.svc.RejectVersion(e.ctx(), v3.ID, admin, "Нарушает правила")
	if err != nil || rejected.Status != domain.VersionRejected {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action LIKE 'post.version_%'`); n != 8 {
		t.Fatalf("audit entries %d", n)
	}
}

func TestPurgeMedia(t *testing.T) {
	e := newEnv(t)
	gone := e.upload(imagingtest.JPEG(300, 200, 0))
	c := e.readyDraft(domain.CarrierMedia)
	d, err := e.svc.GetDraft(e.ctx(), e.org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	inVersion := d.Media[0].File
	if _, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1); err != nil {
		t.Fatal(err)
	}
	e.drain()
	// Устаревшая плашка: реквизиты сменились, черновик пересохранён — старая производная вне версий.
	stale := e.upload(imagingtest.OpaquePNG(300, 200))
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), stale.File.ID)); err != nil {
		t.Fatal(err)
	}
	e.drain()
	e.req.set(e.org, "ООО Новое", "7707083893")
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), stale.File.ID)); err != nil {
		t.Fatal(err)
	}
	e.drain()
	for _, id := range []string{gone.File.ID.String(), inVersion.ID.String()} {
		if err := e.svc.Delete(e.ctx(), e.org, e.user, mustUUID(id)); err != nil {
			t.Fatal(err)
		}
	}

	e.clk.Advance(31 * 24 * time.Hour)
	// Очищаются производные старше срока вне версий — и устаревшая плашка «ООО Ромашка»,
	// и нужная черновику «ООО Новое»: её заново поставит предпросмотр.
	if err := e.svc.PurgeMedia(e.ctx()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.files.Object(gone.File.StorageKey); ok {
		t.Fatal("deleted file without versions not purged")
	}
	if _, ok := e.files.Object(inVersion.StorageKey); !ok {
		t.Fatal("file in a version purged")
	}
	if n := e.count(`SELECT count(*) FROM media_file WHERE purged_at IS NOT NULL`); n != 1 {
		t.Fatalf("purged files %d", n)
	}
	staleLabeled := e.count(`SELECT count(*) FROM media_derivative d WHERE d.kind = 'labeled' AND d.purged_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM post_version_media m WHERE m.media_derivative_id = d.id)`)
	if staleLabeled == 0 {
		t.Fatal("stale labeled derivative not purged")
	}
	if n := e.count(`SELECT count(*) FROM media_derivative d JOIN post_version_media m ON m.media_derivative_id = d.id
		WHERE d.purged_at IS NOT NULL`); n != 0 {
		t.Fatalf("derivatives in versions purged: %d", n)
	}
	// Предпросмотр снова ставит очищенную нужную производную к построению.
	pv, err := e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || !slices.Contains(codes(pv.Problems), "media_processing") {
		t.Fatalf("preview after purge: %v %v", codes(pv.Problems), err)
	}
	e.drain()
	pv, _ = e.svc.Preview(e.ctx(), e.org, c.ID)
	if len(pv.Problems) != 0 || len(pv.MediaURLs) != 1 {
		t.Fatalf("preview after rebuild: %v %v", codes(pv.Problems), pv.MediaURLs)
	}
	// Повтор очистки ничего не делает.
	if err := e.svc.PurgeMedia(e.ctx()); err != nil {
		t.Fatal(err)
	}
}
