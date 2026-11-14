package bootstrap

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
)

func TestChannelSaveHTTP(t *testing.T) {
	a := newTestApp(t)
	blg := a.bloggerClient("atomic-channel@example.ru")
	id := a.moderatedChannel(blg, -80000000821, 100)
	path := "/blogger/channels/" + id
	prices := []map[string]any{{"format_id": "f_2_48", "amount": 400000, "is_enabled": true}}
	var saved apigen.BloggerChannel
	r := blg.do(http.MethodPatch, path, map[string]any{"description": "saved together", "prices": prices})
	r.json(t, &saved)
	if r.status != http.StatusOK || saved.Description == nil || *saved.Description != "saved together" || len(saved.Prices) != 1 || saved.Prices[0].FormatId != "f_2_48" {
		t.Fatalf("atomic response: %d %s", r.status, r.body)
	}
	for _, bad := range []map[string]any{
		{"description": "must roll back", "prices": []map[string]any{{"format_id": "f_1_24", "amount": 1, "is_enabled": true}}},
		{"category_id": uuid.NewString(), "prices": []map[string]any{{"format_id": "f_1_24", "amount": 300000, "is_enabled": true}}},
		{"description": "must roll back", "prices": nil},
	} {
		if r := blg.do(http.MethodPatch, path, bad); r.status != http.StatusUnprocessableEntity {
			t.Fatalf("invalid: %d %s", r.status, r.body)
		}
		r := blg.do(http.MethodGet, path, nil)
		var got apigen.BloggerChannel
		r.json(t, &got)
		if got.Description == nil || *got.Description != *saved.Description || len(got.Prices) != 1 || got.Prices[0].FormatId != "f_2_48" {
			t.Fatalf("partial result: %s", r.body)
		}
	}
	// Old PATCH preserves prices, explicit empty replaces the set on moderation.
	r = blg.do(http.MethodPatch, path, map[string]any{"description": "legacy patch"})
	r.json(t, &saved)
	if r.status != http.StatusOK || len(saved.Prices) != 1 {
		t.Fatalf("old PATCH: %d %s", r.status, r.body)
	}
	r = blg.do(http.MethodPatch, path, map[string]any{"prices": []any{}})
	r.json(t, &saved)
	if r.status != http.StatusOK || len(saved.Prices) != 0 {
		t.Fatalf("empty prices: %d %s", r.status, r.body)
	}
	// Legacy PUT remains available.
	if r := blg.do(http.MethodPut, path+"/prices", map[string]any{"prices": prices}); r.status != http.StatusOK {
		t.Fatalf("old PUT: %d %s", r.status, r.body)
	}
	channeltest.Approve(t, a.db.Pool, uuidOf(t, id))
	r = blg.do(http.MethodPatch, path, map[string]any{"prices": []any{}, "rkn_number": "different"})
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("active empty: %d %s", r.status, r.body)
	}
	other := a.bloggerClient("atomic-foreign@example.ru")
	if r := other.do(http.MethodPatch, path, map[string]any{"prices": prices}); r.status != http.StatusNotFound {
		t.Fatalf("foreign: %d %s", r.status, r.body)
	}
	for _, status := range []string{"blocked", "disconnected"} {
		if _, err := a.db.Owner.Exec(t.Context(), `UPDATE channel SET status=$2 WHERE id=$1`, id, status); err != nil {
			t.Fatal(err)
		}
		if r := blg.do(http.MethodPatch, path, map[string]any{"description": "invalid state", "prices": prices}); r.status != http.StatusConflict {
			t.Fatalf("%s: %d %s", status, r.status, r.body)
		}
	}
	// An unclaimed channel has no organization by database invariant: cabinet
	// access is 404, while domain editing is independently rejected as not editable.
	if _, err := a.db.Owner.Exec(t.Context(), `UPDATE channel SET status='unclaimed', organization_id=NULL, unclaimed_since=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if r := blg.do(http.MethodPatch, path, map[string]any{"prices": prices}); r.status != http.StatusNotFound {
		t.Fatalf("unowned channel: %d %s", r.status, r.body)
	}
}
