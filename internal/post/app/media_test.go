package app_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestUploadJPEG(t *testing.T) {
	e := newEnv(t)
	data := imagingtest.JPEG(800, 600, 6)
	v, err := e.svc.Upload(e.ctx(), e.org, e.user, "../секретное имя.jpg", data)
	if err != nil {
		t.Fatal(err)
	}
	f := v.File
	// R-3: размеры после поворота, имя очищено от пути.
	if f.Mime != domain.MimeJPEG || f.Width != 600 || f.Height != 800 || f.OriginalName != "секретное имя.jpg" || f.SizeBytes != int64(len(data)) {
		t.Fatalf("%+v", f)
	}
	obj, ok := e.files.Object(f.StorageKey)
	if !ok || obj.ContentType != domain.MimeJPEG || string(obj.Data) != string(data) {
		t.Fatal("original not stored byte for byte")
	}
	if !strings.HasPrefix(f.StorageKey, "media/") || !strings.Contains(v.PreviewURL, url.PathEscape(f.StorageKey)) {
		t.Fatalf("preview url before build must lead to the original: %s", v.PreviewURL)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind = 'build_media_derivative' AND status = 'pending'`); n != 1 {
		t.Fatalf("preview jobs: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'media.uploaded'`); n != 1 {
		t.Fatalf("audit: %d", n)
	}

	// Повтор того же файла — та же запись, второго объекта нет.
	again := e.upload(data)
	if again.File.ID != f.ID || e.files.Len() != 1 {
		t.Fatalf("dedupe: %s vs %s, objects %d", again.File.ID, f.ID, e.files.Len())
	}

	// Задание строит превью; ссылка теперь на него. Повтор задания ничего не меняет.
	e.drain()
	list, err := e.svc.List(e.ctx(), e.org, "", 20)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if strings.Contains(list.Items[0].PreviewURL, url.PathEscape(f.StorageKey)) || !strings.Contains(list.Items[0].PreviewURL, "derivative") {
		t.Fatalf("preview url after build: %s", list.Items[0].PreviewURL)
	}
	if e.files.Len() != 2 {
		t.Fatalf("objects after build: %d", e.files.Len())
	}
	var derivID string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT id FROM media_derivative WHERE kind = 'preview'`).Scan(&derivID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.BuildDerivative(context.Background(), buildPayload(t, derivID, e.org)); err != nil || e.files.Len() != 2 {
		t.Fatalf("repeat build: %v, objects %d", err, e.files.Len())
	}
	e.noSecrets("секретное имя")
}

func TestUploadRejects(t *testing.T) {
	e := newEnv(t)
	// Тип — по содержимому: PNG с расширением .jpg принимается как PNG.
	v, err := e.svc.Upload(e.ctx(), e.org, e.user, "a.jpg", imagingtest.OpaquePNG(200, 200))
	if err != nil || v.File.Mime != domain.MimePNG || !strings.HasSuffix(v.File.StorageKey, ".png") {
		t.Fatalf("png named .jpg: %+v %v", v.File, err)
	}
	for name, tt := range map[string]struct {
		data   []byte
		reason string
	}{
		"text":          {[]byte("это текст, а не картинка, хотя имя .jpg"), "unsupported_type"},
		"empty":         {nil, "empty"},
		"animated png":  {imagingtest.AnimatedPNG(200, 200), "animated"},
		"animated webp": {imagingtest.AnimatedWebP(), "animated"},
		"too small":     {imagingtest.JPEG(159, 300, 0), "too_small"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.Upload(e.ctx(), e.org, e.user, "a.jpg", tt.data)
			wantReason(t, err, apperr.CodeValidationFailed, tt.reason)
		})
	}
	if e.files.Len() != 1 {
		t.Fatalf("rejected files stored: %d", e.files.Len())
	}
}

func TestUploadRateLimit(t *testing.T) {
	e := newEnv(t)
	for range 60 {
		if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO audit_log (id, actor_type, actor_user_id, action, object_type, object_id, data, created_at)
			VALUES ($1, 'advertiser', $2, 'media.uploaded', 'media_file', 'x', '{}', $3)`, ids.New(), e.user, start.Add(-30*60e9)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.svc.Upload(e.ctx(), e.org, e.user, "a.jpg", imagingtest.JPEG(200, 200, 0))
	wantReason(t, err, apperr.CodeRateLimited, "")
	// Через час окно сдвигается.
	e.clk.Advance(31 * 60e9)
	if _, err := e.svc.Upload(e.ctx(), e.org, e.user, "a.jpg", imagingtest.JPEG(200, 200, 0)); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestUploadStorageFailure(t *testing.T) {
	e := newEnv(t)
	e.files.FailWith(storagefake.OpPut, errors.New("storage is down"))
	_, err := e.svc.Upload(e.ctx(), e.org, e.user, "a.jpg", imagingtest.JPEG(200, 200, 0))
	if err == nil || apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("storage failure: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM media_file`); n != 0 {
		t.Fatalf("media_file rows: %d", n)
	}
}

func TestDeleteMedia(t *testing.T) {
	e := newEnv(t)
	a := e.upload(imagingtest.JPEG(300, 200, 0))
	b := e.upload(imagingtest.OpaquePNG(300, 200))
	text := doc("Текст")
	var campaigns []string
	for range 2 {
		c := e.campaign("Кампания")
		campaigns = append(campaigns, c.ID.String())
		if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(text, a.File.ID, b.File.ID)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Delete(e.ctx(), e.org, e.user, a.File.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range campaigns {
		d, err := e.svc.GetDraft(e.ctx(), e.org, mustUUID(id))
		if err != nil || len(d.Media) != 1 || d.Media[0].File.ID != b.File.ID {
			t.Fatalf("draft after delete: %+v %v", d.Media, err)
		}
	}
	list, err := e.svc.List(e.ctx(), e.org, "", 20)
	if err != nil || len(list.Items) != 1 || list.Items[0].File.ID != b.File.ID {
		t.Fatalf("list after delete: %+v %v", list.Items, err)
	}
	if err := e.svc.Delete(e.ctx(), e.org, e.user, a.File.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	other, otherUser := posttest.Advertiser(t, e.d.Pool)
	if err := e.svc.Delete(e.ctx(), other, otherUser, b.File.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
}

func TestListPaging(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.upload(imagingtest.JPEG(200+i, 200, 0))
		e.clk.Advance(1e9)
	}
	page, err := e.svc.List(e.ctx(), e.org, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	seen := map[string]bool{}
	for cursor := ""; ; {
		p, err := e.svc.List(e.ctx(), e.org, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range p.Items {
			seen[v.File.ID.String()] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("seen %d files", len(seen))
	}
	if _, err := e.svc.List(e.ctx(), e.org, "!!!", 2); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad cursor: %v", err)
	}
}
