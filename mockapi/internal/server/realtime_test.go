package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"maxim/mockapi/internal/store"
)

func assertSSEReady(t *testing.T, c *http.Client, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "text/event-stream" || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("SSE status=%d headers=%v", r.StatusCode, r.Header)
	}
	scan := bufio.NewScanner(r.Body)
	for _, want := range []string{"event: ready", `data: {"version":1}`, ""} {
		if !scan.Scan() || scan.Text() != want {
			t.Fatalf("SSE want=%q got=%q err=%v", want, scan.Text(), scan.Err())
		}
	}
}

func TestSSEMutationsResetClockAndAccess(t *testing.T) {
	e := newEnv(t)
	c := e.client(adv)
	assertSSEReady(t, c, e.srv.URL+"/api/v1/events")
	assertSSEReady(t, e.client(adm), e.srv.URL+"/api/v1/admin/events")
	if r := e.do(c, "GET", "/admin/events", nil, ""); r.status != 403 {
		t.Fatalf("admin denied=%d", r.status)
	}
	// Store subscriptions let tests inspect deterministic frames without waiting on the network.
	tx, done := e.st.Begin(t.Context())
	token := tx.CreateSession(findUser(t, tx, adv))
	done()
	s, err := e.st.Subscribe(token, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, done = e.st.Begin(t.Context())
	for _, n := range tx.D.Notifications {
		if n.UserID == findUser(t, tx, adv) {
			now := tx.Now
			n.ReadAt = &now
			break
		}
	}
	done()
	select {
	case frame := <-s.Events:
		b, _ := json.Marshal(frame.Data)
		var event store.Invalidation
		if json.Unmarshal(b, &event) != nil || event.Topic != "notification.changed" {
			t.Fatal(string(b))
		}
	case <-time.After(time.Second):
		t.Fatal("no deterministic mutation invalidation")
	}
	e.st.Reset(t.Context())
	select {
	case frame := <-s.Events:
		if frame.Name != "resync_required" {
			t.Fatal(frame.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("reset did not resync")
	}
	e.do(e.client(adm), "POST", "/_mock/clock", map[string]any{"hours": 1}, "")
	found := false
	for len(s.Events) > 0 {
		if (<-s.Events).Name == "resync_required" {
			found = true
		}
	}
	if !found {
		t.Fatal("clock shift did not resync")
	}
	tx, done = e.st.Begin(t.Context())
	tx.DeleteSession(token)
	done()
	if s.Valid() {
		t.Fatal("logout retained stream access")
	}
}

func findUser(t *testing.T, tx *store.Tx, email string) uuid.UUID {
	t.Helper()
	for _, u := range tx.D.Users {
		if u.Email == email {
			return u.ID
		}
	}
	t.Fatal("no fixture user")
	return uuid.Nil
}
