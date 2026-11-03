package app_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestCreateCampaignHasEmptyDraft(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("Осенняя распродажа")
	if c.Status != domain.CampaignDraft {
		t.Fatalf("%+v", c)
	}
	d, err := e.svc.GetDraft(e.ctx(), e.org, c.ID)
	if err != nil || d.Post.Draft.TokenCarrier != domain.CarrierText || d.Post.Draft.TokenPosition != domain.PositionBelow ||
		len(d.Post.Draft.Doc.Blocks) != 0 || len(d.Media) != 0 {
		t.Fatalf("draft %+v %v", d.Post.Draft, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'campaign.created'`); n != 1 {
		t.Fatalf("audit %d", n)
	}
}

func TestSaveDraftAutolinkAndIdempotence(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	in := doc("Подробности: https://shop.example.test/sale.\r\nДо встречи")
	v, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(in))
	if err != nil {
		t.Fatal(err)
	}
	got := v.Post.Draft.Doc
	if !domain.HasLinks(got) || domain.LinkHrefs(got)[0] != "https://shop.example.test/sale" {
		t.Fatalf("autolink: %+v", got)
	}
	if strings.Contains(domain.PlainText(got), "\r") {
		t.Fatal("CR kept")
	}
	// Повторное сохранение того же (уже нормализованного) документа ничего не меняет.
	again, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(got))
	if err != nil || !reflect.DeepEqual(again.Post.Draft.Doc, got) {
		t.Fatalf("second save changed doc: %+v %v", again.Post.Draft.Doc, err)
	}
	e.noSecrets("shop.example.test", "Подробности")
}

func TestSaveDraftRejects(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	other, otherUser := posttest.Advertiser(t, e.d.Pool)
	foreign, err := e.svc.Upload(e.ctx(), other, otherUser, "a.jpg", imagingtest.JPEG(200, 200, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), foreign.File.ID))
	wantReason(t, err, apperr.CodeValidationFailed, "media_not_found")

	mine := e.upload(imagingtest.JPEG(200, 200, 0))
	_, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), mine.File.ID, mine.File.ID))
	wantReason(t, err, apperr.CodeValidationFailed, "duplicate_media")

	many := make([]uuid.UUID, 11)
	for i := range many {
		many[i] = uuid.New()
	}
	_, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), many...))
	wantReason(t, err, apperr.CodeValidationFailed, "too_many_media")

	bad := domain.Doc{Blocks: []domain.Block{{Type: domain.BlockParagraph, Content: []domain.Text{
		{Text: "a"}, {Text: "b", Marks: []domain.Mark{{Type: domain.MarkLink, Href: "http://insecure.test"}}}}}}}
	_, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(bad))
	wantReason(t, err, apperr.CodeValidationFailed, domain.ReasonInvalidDoc)
	if ae, _ := apperr.As(err); ae.Details["path"] != "content.0.content.1.marks.0" || ae.Details["rule"] != domain.RuleHrefInvalid {
		t.Fatalf("details %v", ae.Details)
	}

	_, err = e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc(strings.Repeat("я", 8001))))
	wantReason(t, err, apperr.CodeValidationFailed, "too_long")

	if _, err := e.svc.SaveDraft(e.ctx(), other, otherUser, c.ID, draftInput(doc("x"))); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign campaign: %v", err)
	}
}

func TestPreviewProblems(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	img := e.upload(imagingtest.JPEG(800, 600, 0))
	e.drain()

	// Изображения есть: по умолчанию пометка на плашке, пока плашка строится — media_processing.
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Скидки https://shop.example.test"), img.File.ID)); err != nil {
		t.Fatal(err)
	}
	pv, err := e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || !reflect.DeepEqual(codes(pv.Problems), []string{"media_processing"}) || len(pv.MediaURLs) != 0 {
		t.Fatalf("before build: %v %v %v", codes(pv.Problems), pv.MediaURLs, err)
	}
	e.drain()
	pv, err = e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || len(pv.Problems) != 0 || len(pv.MediaURLs) != 1 {
		t.Fatalf("after build: %v %v %v", codes(pv.Problems), pv.MediaURLs, err)
	}
	if !strings.Contains(pv.Body, "utm_campaign=order_0000000000") || !strings.HasSuffix(pv.FinalTextSample, "erid: "+domain.SampleErid) ||
		strings.Contains(pv.FinalTextSample, "Реклама.") || pv.BodyLimit != 3800 {
		t.Fatalf("body %q\nsample %q", pv.Body, pv.FinalTextSample)
	}
	plain := domain.Length(domain.Serialize(doc("Скидки https://shop.example.test"), nil))
	if pv.BodyLength <= plain {
		t.Fatalf("body length with utm %d must exceed %d", pv.BodyLength, plain)
	}

	// media без изображений, link без ссылок.
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст без ссылок"))); err != nil {
		t.Fatal(err)
	}
	carrier, position := domain.CarrierMedia, domain.PositionLink
	in := draftInput(doc("Текст без ссылок"))
	in.TokenCarrier, in.TokenPosition = &carrier, &position
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, in); err != nil {
		t.Fatal(err)
	}
	pv, _ = e.svc.Preview(e.ctx(), e.org, c.ID)
	if got := codes(pv.Problems); !slices.Contains(got, "media_required_for_token") || !slices.Contains(got, "link_required_for_token") {
		t.Fatalf("token problems: %v", got)
	}

	// 3801 единица — body_too_long, черновик сохранён.
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc(strings.Repeat("я", 3801)))); err != nil {
		t.Fatalf("long draft must be saved: %v", err)
	}
	pv, _ = e.svc.Preview(e.ctx(), e.org, c.ID)
	if !slices.Contains(codes(pv.Problems), "body_too_long") {
		t.Fatalf("long body problems: %v", codes(pv.Problems))
	}

	// Пометка в тексте: строка маркировки с названием и ИНН.
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"))); err != nil {
		t.Fatal(err)
	}
	pv, _ = e.svc.Preview(e.ctx(), e.org, c.ID)
	if !strings.Contains(pv.FinalTextSample, "Реклама. "+posttest.Name+", ИНН 7707083893.") || len(pv.Problems) != 0 {
		t.Fatalf("text marking: %q %v", pv.FinalTextSample, codes(pv.Problems))
	}

	// Нет реквизитов — requisites_missing.
	e.req.set(e.org, "", "")
	pv, _ = e.svc.Preview(e.ctx(), e.org, c.ID)
	if !slices.Contains(codes(pv.Problems), "requisites_missing") {
		t.Fatalf("requisites: %v", codes(pv.Problems))
	}
	e.noSecrets("7707083893", posttest.Name)
}

func TestRequisitesChangeRebuildsPlate(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	img := e.upload(imagingtest.JPEG(400, 300, 0))
	save := func() {
		if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Текст"), img.File.ID)); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	save()
	hashes := func() []string {
		rows, err := e.d.Pool.Query(e.ctx(), `SELECT params_hash FROM media_derivative WHERE kind = 'labeled' ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var h string
			_ = rows.Scan(&h)
			out = append(out, h)
		}
		return out
	}
	if h := hashes(); len(h) != 1 {
		t.Fatalf("labeled derivatives: %v", h)
	}
	e.req.set(e.org, "ООО Василёк", "7707083893")
	e.clk.Advance(time.Minute)
	save()
	h := hashes()
	if len(h) != 2 || h[0] == h[1] {
		t.Fatalf("labeled derivatives after rename: %v", h)
	}
	pv, err := e.svc.Preview(e.ctx(), e.org, c.ID)
	if err != nil || len(pv.MediaURLs) != 1 || len(pv.Problems) != 0 {
		t.Fatalf("preview after rename: %v %v", codes(pv.Problems), err)
	}
}

func TestUpdateCampaign(t *testing.T) {
	e := newEnv(t)
	c := e.campaign("К")
	yesterday := domain.NewDate(2026, 10, 15)
	_, err := e.svc.UpdateCampaign(e.ctx(), e.org, e.user, c.ID, domain.CampaignInput{DateFrom: &yesterday})
	wantReason(t, err, apperr.CodeValidationFailed, domain.ReasonPeriodInPast)

	from, to := domain.NewDate(2026, 10, 20), domain.NewDate(2026, 10, 22)
	extra := "секретные требования"
	got, err := e.svc.UpdateCampaign(e.ctx(), e.org, e.user, c.ID, domain.CampaignInput{Name: ptr("Новое"), DateFrom: &from, DateTo: &to,
		ExtraRequirements: &extra})
	if err != nil || got.Name != "Новое" || !got.DateTo.Equal(to) {
		t.Fatalf("%+v %v", got, err)
	}
	var data string
	if err := e.d.Pool.QueryRow(e.ctx(), `SELECT data::text FROM audit_log WHERE action = 'campaign.updated'`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, extra) || !strings.Contains(data, "date_from") {
		t.Fatalf("audit data %s", data)
	}
	e.markOrdered(c.ID)
	_, err = e.svc.UpdateCampaign(e.ctx(), e.org, e.user, c.ID, domain.CampaignInput{DateTo: &from})
	wantReason(t, err, apperr.CodeConflict, "not_draft")
	if _, err := e.svc.UpdateCampaign(e.ctx(), e.org, e.user, c.ID, domain.CampaignInput{Name: ptr("Ещё")}); err != nil {
		t.Fatalf("rename ordered: %v", err)
	}

	page, err := e.svc.ListCampaigns(e.ctx(), e.org, nil, "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "Ещё" {
		t.Fatalf("list: %+v %v", page, err)
	}
	other, _ := posttest.Advertiser(t, e.d.Pool)
	if _, err := e.svc.GetCampaign(e.ctx(), other, c.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign: %v", err)
	}
}
