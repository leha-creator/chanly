package bootstrap

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
)

func TestAdminCategoriesLifecycleHTTP(t *testing.T) {
	a := newTestApp(t)
	senior := a.adminClient("category-senior@example.ru", accountdomain.AdminSenior)
	mod := a.adminClient("category-mod@example.ru", accountdomain.AdminModerator)
	blg := a.bloggerClient("category-blogger@example.ru")
	var category apigen.AdminChannelCategory
	slug := "test-" + uuid.NewString()
	r := senior.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": slug, "name": "Новая категория", "position": 9000})
	r.json(t, &category)
	if r.status != http.StatusCreated || category.Archived {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	defer func() {
		_, _ = a.db.Owner.Exec(t.Context(), `DELETE FROM channel WHERE category_id=$1`, category.Id)
		_, _ = a.db.Owner.Exec(t.Context(), `DELETE FROM channel_category WHERE id=$1`, category.Id)
	}()
	for _, c := range []*client{a.client(), mod, blg} {
		want := http.StatusForbidden
		if c != mod && c != blg {
			want = http.StatusUnauthorized
		}
		if r := c.do(http.MethodGet, "/admin/channel-categories", nil); r.status != want {
			t.Fatalf("role: %d %s", r.status, r.body)
		}
	}
	if r := senior.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": slug, "name": "Дубль"}); r.status != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", r.status, r.body)
	} else if _, details := r.errorCode(t); details["reason"] != "slug_taken" {
		t.Fatalf("duplicate reason: %s", r.body)
	}
	for _, bad := range []map[string]any{{"slug": "BAD!", "name": "Bad"}, {"slug": "valid", "name": " "}} {
		if r := senior.do(http.MethodPost, "/admin/channel-categories", bad); r.status != http.StatusUnprocessableEntity {
			t.Fatalf("invalid: %d %s", r.status, r.body)
		}
	}
	id := a.moderatedChannel(blg, -80000000901, 100)
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{"category_id": category.Id}); r.status != http.StatusOK {
		t.Fatalf("select: %d %s", r.status, r.body)
	}
	for _, archived := range []bool{false, true, false} {
		r := senior.do(http.MethodPatch, "/admin/channel-categories/"+category.Id.String(), map[string]any{"name": "Изменено", "position": 8000, "archived": archived})
		if r.status != http.StatusOK {
			t.Fatalf("update: %d %s", r.status, r.body)
		}
		r = senior.do(http.MethodGet, "/admin/channel-categories", nil)
		var all struct {
			Items []apigen.AdminChannelCategory `json:"items"`
		}
		r.json(t, &all)
		if r.status != http.StatusOK {
			t.Fatalf("admin list: %d %s", r.status, r.body)
		}
		found := false
		for i, item := range all.Items {
			if i > 0 {
				prev := all.Items[i-1]
				if prev.Position > item.Position || (prev.Position == item.Position && prev.Slug > item.Slug) {
					t.Fatal("unstable category sort")
				}
			}
			if item.Id == category.Id {
				found = true
				if item.Archived != archived || item.Name != "Изменено" || item.Position != 8000 || item.Slug != slug {
					t.Fatalf("admin item: %+v", item)
				}
			}
		}
		if !found {
			t.Fatal("category absent in admin")
		}
		r = blg.do(http.MethodGet, "/channel-categories", nil)
		var public struct {
			Items []apigen.ChannelCategory `json:"items"`
		}
		r.json(t, &public)
		found = false
		for _, item := range public.Items {
			if item.Id == category.Id {
				found = true
			}
		}
		if r.status != http.StatusOK || found == archived {
			t.Fatalf("public archive visibility: %d %s", r.status, r.body)
		}
		// Editing other fields preserves an archived relation, with no hidden replacement.
		r = blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{"description": "Редактирование с прежней категорией"})
		var channel apigen.BloggerChannel
		r.json(t, &channel)
		if r.status != http.StatusOK || channel.CategoryId == nil || *channel.CategoryId != category.Id {
			t.Fatalf("preserve relation: %d %s", r.status, r.body)
		}
		r = blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{"category_id": category.Id})
		want := http.StatusOK
		if archived {
			want = http.StatusUnprocessableEntity
		}
		if r.status != want {
			t.Fatalf("select archived: %d %s", r.status, r.body)
		}
	}
}
