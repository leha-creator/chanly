package max_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
)

func TestListMessages(t *testing.T) {
	var got url.Values
	file := "messages.json"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		got = r.URL.Query()
		if got.Get("chat_id") == "403" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"chat.denied","message":"bot is not an admin"}`))
			return
		}
		_, _ = w.Write(testdata(t, file))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	ctx := context.Background()
	to := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	from := to.Add(-30 * 24 * time.Hour)

	list, err := c.ListMessages(ctx, -70000000001, from, to, 100)
	if err != nil || len(list) != 3 {
		t.Fatalf("messages %+v err %v", list, err)
	}
	// В MAX from — верхняя граница, to — нижняя, миллисекунды Unix.
	if got.Get("chat_id") != "-70000000001" || got.Get("count") != "100" ||
		got.Get("from") != "1792141200000" || got.Get("to") != "1789549200000" {
		t.Fatalf("query %v", got)
	}
	if list[0].MID != "mid.3" || !list[0].FromBot || list[0].Views == nil || *list[0].Views != 2100 ||
		!list[0].At.Equal(time.UnixMilli(1760598000000)) {
		t.Fatalf("first %+v", list[0])
	}
	if list[1].FromBot || list[2].Views == nil || *list[2].Views != 0 {
		t.Fatalf("rest %+v %+v", list[1], list[2])
	}

	file = "messages_no_stat.json"
	list, err = c.ListMessages(ctx, -70000000001, from, to, 100)
	if err != nil || len(list) != 2 || list[0].Views != nil || list[1].Views != nil {
		t.Fatalf("no stat: %+v %v", list, err)
	}
	file = "messages_empty.json"
	if list, err = c.ListMessages(ctx, -70000000001, from, to, 100); err != nil || len(list) != 0 {
		t.Fatalf("empty: %+v %v", list, err)
	}
	if _, err := c.ListMessages(ctx, 403, from, to, 100); !job.IsPermanent(err) {
		t.Fatalf("403: want permanent, got %v", err)
	}
	if _, err := c.ListMessages(ctx, 1, from, to, 101); !job.IsPermanent(err) {
		t.Fatalf("count 101: want permanent, got %v", err)
	}
}
