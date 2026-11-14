package bootstrap

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/ids"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

func TestCampaignRequirementsDatesAndParagraphsHTTP(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("campaign-contract@example.ru")
	// The same instant is still Oct 16 in UTC and already Oct 17 in UTC+4.
	// Calendar validation uses MSK, regardless of the client's representation.
	a.clk.Set(time.Date(2026, 10, 17, 0, 59, 0, 0, time.FixedZone("client", 4*3600)))
	r := c.do(http.MethodPost, "/campaigns", map[string]any{"name": "Boundary", "date_from": "2026-10-16", "date_to": "2026-10-18", "time_from": "10:00", "time_to": "20:00"})
	var campaign apigen.Campaign
	r.json(t, &campaign)
	if r.status != http.StatusCreated {
		t.Fatalf("before MSK midnight: %d %s", r.status, r.body)
	}
	path := "/campaigns/" + campaign.Id.String()
	for _, text := range []string{strings.Repeat("😀", 2000), ""} {
		r := c.do(http.MethodPatch, path, map[string]any{"extra_requirements": text})
		var got apigen.Campaign
		r.json(t, &got)
		if r.status != http.StatusOK || got.ExtraRequirements == nil || *got.ExtraRequirements != text {
			t.Fatalf("requirements roundtrip: %d", r.status)
		}
	}
	for _, bad := range []any{strings.Repeat("😀", 2001), nil} {
		if r := c.do(http.MethodPatch, path, map[string]any{"extra_requirements": bad}); r.status != http.StatusUnprocessableEntity {
			t.Fatalf("bad requirements: %d", r.status)
		}
	}
	doc := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Первая строка"}}},
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Вторая строка"}}},
		map[string]any{"type": "paragraph", "content": []any{}},
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "После пустой строки"}}},
	}}
	if r := c.do(http.MethodPut, path+"/post", map[string]any{"content_doc": doc, "media_file_ids": []any{}}); r.status != http.StatusOK {
		t.Fatalf("save post: %d %s", r.status, r.body)
	}
	r = c.do(http.MethodPost, path+"/post/preview", nil)
	var preview struct {
		Body string `json:"body"`
	}
	r.json(t, &preview)
	want := "Первая строка\nВторая строка\n\nПосле пустой строки"
	if r.status != http.StatusOK || preview.Body != want || strings.Contains(preview.Body, "<p>") {
		t.Fatalf("preview paragraphs: %d %q", r.status, preview.Body)
	}
	creator := ids.New()
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT created_by FROM campaign WHERE id=$1`, campaign.Id).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	version, err := a.mods.post.CreateVersion(t.Context(), c.orgID(t), campaign.Id, creator, 1001)
	// Persisted version and MAX final text keep the same paragraph breaks.
	if err != nil {
		t.Fatal(err)
	}
	if version.Body != want {
		t.Fatalf("version payload differs: %q", version.Body)
	}
	version.Status = postdomain.VersionApproved
	if err := version.MarkReady("test-erid", 4000); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(version.FinalText, want) || strings.Contains(version.FinalText, "<p>") {
		t.Fatal("MAX final text changed paragraph breaks")
	}
	a.clk.Advance(2 * time.Minute)
	r = c.do(http.MethodPost, "/campaigns", map[string]any{"name": "Stale form", "date_from": "2026-10-16"})
	if _, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || details["reason"] != "period_in_past" {
		t.Fatalf("stale create: %d %s", r.status, r.body)
	}
	// A newly submitted past period is rejected on PATCH too.
	r = c.do(http.MethodPatch, path, map[string]any{"date_from": "2026-10-15"})
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("past patch: %d %s", r.status, r.body)
	}
	r = c.do(http.MethodPost, path+"/checkout", nil)
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "campaign_incomplete" {
		t.Fatalf("checkout rechecks date: %d %s", r.status, r.body)
	}
	// Make the campaign ordered through a real fake-funded checkout, preserving
	// campaign_ordered_has_order rather than constructing an invalid DB row.
	if r := c.do(http.MethodPatch, path, map[string]any{"date_from": "2026-10-20", "date_to": "2026-10-22"}); r.status != http.StatusOK {
		t.Fatalf("future period: %d %s", r.status, r.body)
	}
	a.fund("campaign-contract@example.ru", testFunding)
	blogger := a.bloggerClient("campaign-contract-blogger@example.ru")
	channel := a.activeChannel(blogger, "Paragraph contract", 300000)
	if r := c.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": channel, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("cart: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodPost, path+"/checkout", nil); r.status != http.StatusCreated {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	if r := c.do(http.MethodPatch, path, map[string]any{"extra_requirements": "change"}); r.status != http.StatusConflict {
		t.Fatalf("non-draft requirements: %d %s", r.status, r.body)
	}
}
