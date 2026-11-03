package bootstrap

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

type previewView struct {
	Body            string   `json:"body"`
	BodyLength      int      `json:"body_length"`
	FinalTextSample string   `json:"final_text_sample"`
	MediaURLs       []string `json:"media_urls"`
	Problems        []struct {
		Code string `json:"code"`
	} `json:"problems"`
}

func (p previewView) codes() []string {
	out := []string{}
	for _, pr := range p.Problems {
		out = append(out, pr.Code)
	}
	return out
}

// storageObject читает объект поддельного хранилища по подписанной ссылке https://storage.test/<key>?expires=…
func (a *testApp) storageObject(t *testing.T, signed string) image.Image {
	t.Helper()
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	o, ok := a.files.Object(key)
	if !ok {
		t.Fatalf("object %q not found", key)
	}
	img, _, err := image.Decode(bytes.NewReader(o.Data))
	if err != nil {
		t.Fatalf("decode %q: %v", key, err)
	}
	return img
}

// TestPostEndToEnd — S-04 → S-05 на собранных api и worker, затем версия поста
// так, как её создаст этап «Заказ и размещения» (R-18).
func TestPostEndToEnd(t *testing.T) {
	a := newTestApp(t)
	const email = "adv-post@example.ru"
	c := a.advertiserClient(email)

	// Два изображения: JPEG с поворотом EXIF и PNG с прозрачностью.
	upload := func(name string, data []byte) string {
		r := c.do(http.MethodPost, "/media", fileBody(name, data))
		if r.status != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", name, r.status, r.body)
		}
		var f struct {
			ID     string `json:"id"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		r.json(t, &f)
		return f.ID
	}
	jpegID := upload("секретный-отпуск.jpg", imagingtest.JPEG(1200, 800, 6))
	pngID := upload("логотип-тайна.png", imagingtest.TransparentPNG(600, 400))

	var camp struct {
		ID string `json:"id"`
	}
	c.do(http.MethodPost, "/campaigns", map[string]any{"name": "Осенняя распродажа"}).json(t, &camp)

	postText := "Тайная распродажа для своих"
	doc := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "heading", "content": []any{map[string]any{"type": "text", "text": "Осень"}}},
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": postText + " ", "marks": []any{map[string]any{"type": "bold"}}},
			map[string]any{"type": "text", "text": "https://secret-shop.example.test/sale"},
		}},
	}}
	save := func(media []string, carrier, position string) {
		t.Helper()
		body := map[string]any{"content_doc": doc, "media_file_ids": media}
		if carrier != "" {
			body["token_carrier"], body["token_position"] = carrier, position
		}
		if r := c.do(http.MethodPut, "/campaigns/"+camp.ID+"/post", body); r.status != http.StatusOK {
			t.Fatalf("save: %d %s", r.status, r.body)
		}
	}
	preview := func() previewView {
		t.Helper()
		var p previewView
		r := c.do(http.MethodPost, "/campaigns/"+camp.ID+"/post/preview", nil)
		if r.status != http.StatusOK {
			t.Fatalf("preview: %d %s", r.status, r.body)
		}
		r.json(t, &p)
		return p
	}

	// S-04: альбом из двух изображений, по умолчанию пометка на плашке, erid под постом.
	save([]string{jpegID, pngID}, "", "")
	if p := preview(); len(p.codes()) != 1 || p.codes()[0] != "media_processing" {
		t.Fatalf("before jobs: %v", p.codes())
	}
	a.settle()
	p := preview()
	if len(p.Problems) != 0 || len(p.MediaURLs) != 2 {
		t.Fatalf("after jobs: %v %v", p.codes(), p.MediaURLs)
	}
	if !strings.HasSuffix(p.FinalTextSample, "\n\nerid: erid_sample") || strings.Contains(p.FinalTextSample, "Реклама.") ||
		!strings.Contains(p.Body, "utm_campaign=order_0000000000") || !strings.Contains(p.Body, "<h1>Осень</h1>") {
		t.Fatalf("preview text:\n%s\n%s", p.Body, p.FinalTextSample)
	}
	// Плашка на каждом изображении альбома; JPEG повёрнут (800×1200), прозрачный PNG остался PNG.
	rotated := a.storageObject(t, p.MediaURLs[0])
	if b := rotated.Bounds(); b.Dx() != 800 || b.Dy() != 1200 {
		t.Fatalf("rotated plate size %v", b)
	}
	if _, _, _, alpha := a.storageObject(t, p.MediaURLs[1]).At(300, 10).RGBA(); alpha == 0xffff {
		t.Fatal("png transparency lost above the plate")
	}

	// Пометка в тексте, erid над постом: без плашки, строка маркировки сверху.
	save([]string{jpegID, pngID}, "text", "above")
	a.settle()
	p = preview()
	if len(p.Problems) != 0 || len(p.MediaURLs) != 2 || !strings.HasPrefix(p.FinalTextSample, "Реклама. ") ||
		!strings.Contains(strings.SplitN(p.FinalTextSample, "\n", 2)[0], "erid: erid_sample") {
		t.Fatalf("text marking: %v %q", p.codes(), p.FinalTextSample)
	}
	if b := a.storageObject(t, p.MediaURLs[0]).Bounds(); b.Dx() != 800 {
		t.Fatalf("plain copy size %v", b)
	}

	// Удаление файла из медиатеки убирает его из черновика.
	if r := c.do(http.MethodDelete, "/media/"+pngID, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete media: %d", r.status)
	}
	var draft struct {
		Media []struct {
			ID string `json:"id"`
		} `json:"media"`
	}
	c.do(http.MethodGet, "/campaigns/"+camp.ID+"/post", nil).json(t, &draft)
	if len(draft.Media) != 1 || draft.Media[0].ID != jpegID {
		t.Fatalf("draft media after delete: %+v", draft.Media)
	}

	// Версия — как её создаст оформление заказа: номер заказа в UTM, публикация производных.
	save([]string{jpegID}, "media", "below")
	a.settle()
	var orgID, userID uuid.UUID
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT m.organization_id, m.user_id FROM organization_member m
		JOIN "user" u ON u.id = m.user_id WHERE u.email = $1`, email).Scan(&orgID, &userID); err != nil {
		t.Fatal(err)
	}
	svc := a.mods.post
	v, err := svc.CreateVersion(context.Background(), orgID, uuidOf(t, camp.ID), userID, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Body, "utm_campaign=order_1001") || v.Number != 1 {
		t.Fatalf("version body %q", v.Body)
	}
	a.settle()
	view, err := svc.GetVersion(context.Background(), v.ID)
	if err != nil || len(view.MediaURLs) != 1 || !strings.HasPrefix(view.MediaURLs[0], "https://public.storage.test/pub/") {
		t.Fatalf("public urls: %+v %v", view.MediaURLs, err)
	}
	if _, err := svc.ApproveVersion(context.Background(), v.ID, userID); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.MarkVersionReady(context.Background(), v.ID, "2VtzqvXyZ")
	if err != nil || !strings.HasSuffix(ready.FinalText, v.Body+"\n\nerid: 2VtzqvXyZ") {
		t.Fatalf("ready: %q %v", ready.FinalText, err)
	}
	// Правка черновика после версии версию не меняет.
	save([]string{}, "", "")
	after, err := svc.GetVersion(context.Background(), v.ID)
	if err != nil || after.Version.Body != v.Body || len(after.Version.Media) != 1 || after.Version.FinalText != ready.FinalText {
		t.Fatalf("version changed after draft edit: %+v %v", after.Version, err)
	}

	// Журнал: обязательные события этапа есть; текст поста, адреса, реквизиты и имена файлов — нет.
	logs := a.logs.String()
	for _, m := range []string{
		"media uploaded", "media deleted", "media derivative built", "media derivative published", "campaign created",
		"post draft saved", "post preview built", "post version created", "post version status changed",
		"job started", "job finished", "http request finished",
	} {
		if !strings.Contains(logs, `"msg":"`+m+`"`) {
			t.Errorf("required log event %q is missing", m)
		}
	}
	var inn string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT inn FROM organization WHERE id = $1`, orgID).Scan(&inn); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{postText, "secret-shop.example.test", "секретный-отпуск", "логотип-тайна", inn, "Ромашка"} {
		if strings.Contains(logs, s) {
			t.Errorf("log contains %q", s)
		}
	}
}
