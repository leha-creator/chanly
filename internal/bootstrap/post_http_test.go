package bootstrap

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

func TestPostOperationsRequireAdvertiser(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("no-org@example.ru")
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/media"}, {http.MethodGet, "/campaigns"},
		{http.MethodPost, "/campaigns"}, {http.MethodGet, "/campaigns/0190a000-0000-7000-8000-000000000001/post"},
	} {
		var body any
		if op.method == http.MethodPost {
			body = map[string]any{"name": "К"}
		}
		if r := c.do(op.method, op.path, body); r.status != http.StatusForbidden {
			t.Errorf("%s %s without advertiser cabinet: %d %s", op.method, op.path, r.status, r.body)
		}
	}
}

func TestMediaUploadHTTP(t *testing.T) {
	a := newTestApp(t)
	c := a.advertiserClient("media@example.ru")

	r := c.do(http.MethodPost, "/media", fileBody("photo.jpg", imagingtest.JPEG(800, 600, 0)))
	if r.status != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.status, r.body)
	}
	var f struct {
		ID         string `json:"id"`
		PreviewURL string `json:"preview_url"`
		Mime       string `json:"mime"`
	}
	r.json(t, &f)
	if f.PreviewURL == "" || f.Mime != "image/jpeg" {
		t.Fatalf("media file %+v", f)
	}

	// Больше предела — 422 too_large (одно поведение и для файла, и для тела больше предела сервера).
	big := bytes.Repeat([]byte{0xFF}, 50<<20+1)
	r = c.do(http.MethodPost, "/media", fileBody("big.jpg", big))
	if code, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || code != "validation_failed" || details["reason"] != "too_large" {
		t.Fatalf("too large: %d %s", r.status, r.body)
	}
	r = c.do(http.MethodPost, "/media", fileBody("note.jpg", []byte("просто текст")))
	if _, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || details["reason"] != "unsupported_type" {
		t.Fatalf("text: %d %s", r.status, r.body)
	}

	// Без X-CSRF-Token меняющий запрос отклоняется.
	c.csrf = false
	r = c.do(http.MethodPost, "/media", fileBody("photo.jpg", imagingtest.JPEG(300, 300, 0)))
	if _, details := r.errorCode(t); r.status != http.StatusForbidden || details["reason"] != "csrf" {
		t.Fatalf("no csrf: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodDelete, "/media/"+f.ID, nil); r.status != http.StatusForbidden {
		t.Fatalf("delete without csrf: %d", r.status)
	}
	c.csrf = true

	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	c.do(http.MethodGet, "/media?limit=10", nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != f.ID {
		t.Fatalf("list %+v", list)
	}
	if r := c.do(http.MethodDelete, "/media/"+f.ID, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodDelete, "/media/"+f.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("delete twice: %d", r.status)
	}
	// Чужой файл — 404.
	other := a.advertiserClient("other-media@example.ru")
	r = c.do(http.MethodPost, "/media", fileBody("photo.png", imagingtest.OpaquePNG(300, 300)))
	r.json(t, &f)
	if r := other.do(http.MethodDelete, "/media/"+f.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign delete: %d", r.status)
	}
}

func TestCampaignPostHTTP(t *testing.T) {
	a := newTestApp(t)
	c := a.advertiserClient("campaign@example.ru")

	r := c.do(http.MethodPost, "/campaigns", map[string]any{
		"name": "Осенняя распродажа", "date_from": "2026-10-20", "date_to": "2026-10-25", "time_from": "10:00", "time_to": "20:00",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var camp struct {
		ID       string `json:"id"`
		TimeFrom string `json:"time_from"`
	}
	r.json(t, &camp)
	if camp.TimeFrom != "10:00" {
		t.Fatalf("campaign %s", r.body)
	}
	r = c.do(http.MethodPost, "/campaigns", map[string]any{"name": "К", "date_from": "2026-10-20", "date_to": "2026-11-20"})
	if _, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || details["reason"] != "period_too_long" {
		t.Fatalf("long period: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodPatch, "/campaigns/"+camp.ID, map[string]any{"name": nil}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("clearing a field must be rejected: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodPatch, "/campaigns/"+camp.ID, map[string]any{"name": "Новое"}); r.status != http.StatusOK {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}

	// Сохранение возвращает нормализованный документ с распознанной ссылкой.
	doc := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "Скидки "},
			map[string]any{"type": "text", "text": "до 50%", "marks": []any{map[string]any{"type": "bold"}, map[string]any{"type": "bold"}}},
			map[string]any{"type": "text", "text": " https://shop.example.test/sale."},
		}},
	}}
	r = c.do(http.MethodPut, "/campaigns/"+camp.ID+"/post", map[string]any{"content_doc": doc, "media_file_ids": []string{}})
	if r.status != http.StatusOK {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	body := string(r.body)
	if !strings.Contains(body, `"href":"https://shop.example.test/sale"`) || strings.Count(body, `"bold"`) != 1 ||
		!strings.Contains(body, `"token_carrier":"text"`) {
		t.Fatalf("normalized doc: %s", body)
	}
	// Неизвестный тип блока отклоняет проверка запроса до обработчика.
	bad := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "list"}}}
	if r := c.do(http.MethodPut, "/campaigns/"+camp.ID+"/post", map[string]any{"content_doc": bad, "media_file_ids": []string{}}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown block: %d %s", r.status, r.body)
	}

	r = c.do(http.MethodPost, "/campaigns/"+camp.ID+"/post/preview", nil)
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.body)
	}
	var pv struct {
		Body            string `json:"body"`
		FinalTextSample string `json:"final_text_sample"`
		Problems        []any  `json:"problems"`
	}
	r.json(t, &pv)
	if !strings.Contains(pv.Body, "order_0000000000") || !strings.Contains(pv.FinalTextSample, "Реклама.") || len(pv.Problems) != 0 {
		t.Fatalf("preview %s", r.body)
	}
	if r := c.do(http.MethodGet, "/campaigns?limit=5", nil); r.status != http.StatusOK || !strings.Contains(string(r.body), "Новое") {
		t.Fatalf("list: %d %s", r.status, r.body)
	}
	// Оплата с пустой корзиной — 409 cart_changed (этап «Заказ и размещения»).
	if r := c.do(http.MethodPost, "/campaigns/"+camp.ID+"/checkout", nil); r.status != http.StatusConflict {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	other := a.advertiserClient("other-campaign@example.ru")
	if r := other.do(http.MethodGet, "/campaigns/"+camp.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign campaign: %d", r.status)
	}
}
