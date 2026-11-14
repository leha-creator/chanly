package bootstrap

import (
	"net/http"
	"testing"
	"time"
)

func TestPayoutRefreshAsyncHTTP(t *testing.T) {
	a := newTestApp(t)
	c := a.bloggerClient("refresh-async@example.ru")
	var before rocketworkView
	c.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &before)
	if before.StatusCheckedAt == nil {
		t.Fatal("fixture missing checked_at")
	}
	a.clk.Advance(time.Minute)
	for range 2 {
		r := c.do(http.MethodPost, "/blogger/rocketwork/refresh", nil)
		var got rocketworkView
		r.json(t, &got)
		if r.status != http.StatusOK || got.Status != before.Status || got.StatusCheckedAt == nil || *got.StatusCheckedAt != *before.StatusCheckedAt {
			t.Fatalf("refresh is only queued: %d %s", r.status, r.body)
		}
	}
	var jobs int
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='poll_contractor' AND status IN ('pending','running')`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("refresh dedupe: %d %v", jobs, err)
	}
	a.settle()
	var after rocketworkView
	c.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &after)
	if after.Status != before.Status || after.StatusCheckedAt == nil || *after.StatusCheckedAt == *before.StatusCheckedAt {
		t.Fatal("worker did not update checked_at for unchanged status")
	}
	for range 8 {
		if r := c.do(http.MethodPost, "/blogger/rocketwork/refresh", nil); r.status != http.StatusOK {
			t.Fatalf("within limit: %d %s", r.status, r.body)
		}
	}
	r := c.do(http.MethodPost, "/blogger/rocketwork/refresh", nil)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("refresh rate limit: %d %s", r.status, r.body)
	}
	bare, _ := a.newBlogger("refresh-none@example.ru")
	r = bare.do(http.MethodPost, "/blogger/rocketwork/refresh", nil)
	var none rocketworkView
	r.json(t, &none)
	if r.status != http.StatusOK || none.Status != "none" || none.StatusCheckedAt != nil {
		t.Fatalf("none: %d %s", r.status, r.body)
	}
	// Existing active refresh stays compatible; disabled provider returns 503.
	a.mods.payout.Config.ProviderEnabled = false
	r = c.do(http.MethodPost, "/blogger/rocketwork/refresh", nil)
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("rate gate precedence: %d", r.status)
	}
	a.clk.Advance(time.Hour + time.Second)
	r = c.do(http.MethodPost, "/blogger/rocketwork/refresh", nil)
	if code, details := r.errorCode(t); r.status != http.StatusServiceUnavailable || code != "service_unavailable" || details["reason"] != "rocketwork_unavailable" {
		t.Fatalf("disabled refresh: %d %s", r.status, r.body)
	}
}
