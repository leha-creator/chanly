package outbound_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/outbound"
)

func TestSensitiveCallHidesPlainTextAndPathWithoutChangingResponse(t *testing.T) {
	d := dbtest.New(t)
	var logs bytes.Buffer
	status := 400
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("private-body"))
	}))
	defer srv.Close()
	c := outbound.New(d.Pool, clock.NewManual(start), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), outbound.Options{System: outbound.SystemRocketWork})
	for _, code := range []int{400, 200} {
		status = code
		r, err := c.Do(context.Background(), outbound.Call{Operation: "Sensitive", Method: "GET", URL: srv.URL + "/private-path", Sensitive: true, Header: http.Header{"X-Private": {"private-header"}}})
		if (err == nil) != (code == 200) || string(r.Body) != "private-body" {
			t.Fatalf("response changed: %d %v", r.Status, err)
		}
		if err != nil && strings.Contains(err.Error(), "private-") {
			t.Fatal("private error")
		}
	}
	var recorded string
	if err := d.Pool.QueryRow(context.Background(), "SELECT string_agg(request::text || coalesce(response_body::text,'') || coalesce(error,''), ' ') FROM outbound_request").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{recorded, logs.String()} {
		if strings.Contains(text, "private-") {
			t.Fatal("sensitive data leaked")
		}
	}
	// The scoped policy must not mutate the shared client for another call.
	_, _ = c.Do(context.Background(), outbound.Call{Operation: "Ordinary", Method: "GET", URL: srv.URL + "/public-path"})
	if !strings.Contains(logs.String(), "public-path") {
		t.Fatal("redaction leaked across calls")
	}
}
