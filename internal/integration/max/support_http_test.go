package max_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportSendPrivacyAndNoReplay(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"message":"private provider response"}`))
	}))
	defer srv.Close()
	c, d := newClient(t, srv, nil)
	if _, e := c.SendSupportMessage(context.Background(), "123", "private conversation content", nil); e == nil {
		t.Fatal("server failure accepted")
	}
	if calls != 1 {
		t.Fatal("ambiguous POST replayed")
	}
	var stored string
	if e := d.Pool.QueryRow(context.Background(), `SELECT request::text||coalesce(response_body::text,'')||coalesce(error,'') FROM outbound_request LIMIT 1`).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"private conversation content", "private provider response", token} {
		if strings.Contains(stored, secret) {
			t.Fatal("private support data persisted in outbound history")
		}
	}
}
