package bootstrap

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestCatalogCompleteCursorTraversalHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("catalog-pagination@example.ru")
	org := channeltest.Organization(t, a.db.Pool)
	expected := map[string]bool{}
	tx, err := a.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	for i := 0; i < 140; i++ {
		id := ids.New()
		status, rknStatus := "active", "confirmed"
		if i == 137 {
			status = "moderation"
		}
		if i == 138 {
			rknStatus = "pending"
			status = "suspended"
		}
		ready := i != 139
		_, err := tx.Exec(t.Context(), `INSERT INTO channel (id,organization_id,max_chat_id,title,description,is_public,added_by_max_user_id,category_id,status,bot_is_admin,bot_permissions,subscribers_count,rkn_status,rating,ord_platform_ready_at,created_at,updated_at)
   VALUES ($1,$2,$3,$4,'pagination fixture',true,7,$5,$6,true,ARRAY['write','delete'],10000,$7,100,CASE WHEN $8 THEN now() ELSE NULL END,now(),now())`, id, org, int64(-80000010000-i), fmt.Sprintf("feedbackpagination %03d", i), channeltest.CategoryFood, status, rknStatus, ready)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO channel_price(id,channel_id,format_id,amount,is_enabled,created_at,updated_at) VALUES($1,$2,'f_1_24',$3,true,now(),now())`, ids.New(), id, 300000+i%9); err != nil {
			t.Fatal(err)
		}
		if i < 137 {
			expected[id.String()] = true
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"", "100"} {
		for _, sort := range []string{"subscribers_desc", "price_asc"} {
			q := url.Values{"q": {"feedbackpagination"}, "category_id": {channeltest.CategoryFood.String()}, "format_id": {"f_1_24"}, "price_to": {"1000000"}, "sort": {sort}}
			if limit != "" {
				q.Set("limit", limit)
			}
			seen := map[string]bool{}
			cursors := map[string]bool{}
			pages := 0
			for {
				r := adv.do(http.MethodGet, "/catalog/channels?"+q.Encode(), nil)
				var page catalogPage
				r.json(t, &page)
				if r.status != http.StatusOK {
					t.Fatalf("page: %d %s", r.status, r.body)
				}
				pages++
				max := 20
				if limit == "100" {
					max = 100
				}
				if len(page.Items) == 0 || len(page.Items) > max {
					t.Fatalf("page count: %d", len(page.Items))
				}
				for _, item := range page.Items {
					if seen[item.ID] || !expected[item.ID] {
						t.Fatalf("duplicate or ineligible item: %s", item.ID)
					}
					seen[item.ID] = true
				}
				if page.NextCursor == "" {
					break
				}
				if cursors[page.NextCursor] || pages > 10 {
					t.Fatal("cursor loop")
				}
				cursors[page.NextCursor] = true
				q.Set("cursor", page.NextCursor)
			}
			if len(seen) != len(expected) {
				t.Fatalf("limit %q sort %s: %d/%d channels", limit, sort, len(seen), len(expected))
			}
			if limit == "" && pages != 7 || limit == "100" && pages != 2 {
				t.Fatalf("unexpected pages: %d", pages)
			}
			// Every selected identifier remains addressable as an eligible catalog card.
			checked := 0
			for id := range seen {
				if r := adv.do(http.MethodGet, "/catalog/channels/"+id, nil); r.status != http.StatusOK {
					t.Fatalf("selected card: %d %s", r.status, r.body)
				}
				checked++
				if checked == 3 {
					break
				}
			}
		}
	}
}
