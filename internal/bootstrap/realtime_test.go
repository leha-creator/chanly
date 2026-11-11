package bootstrap

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/google/uuid"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/metrics"
	"github.com/leha-creator/chanly/internal/platform/realtime"
)

type sseFrame struct {
	Name string
	Data json.RawMessage
}
type testStream struct {
	Frames chan sseFrame
	Done   chan struct{}
	body   io.ReadCloser
}

func readStream(body io.ReadCloser) *testStream {
	s := &testStream{Frames: make(chan sseFrame, 256), Done: make(chan struct{}), body: body}
	go func() {
		defer close(s.Done)
		scan := bufio.NewScanner(body)
		var frame sseFrame
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "event: ") {
				frame.Name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				frame.Data = append(json.RawMessage(nil), strings.TrimPrefix(line, "data: ")...)
			}
			if line == "" && frame.Name != "" {
				s.Frames <- frame
				frame = sseFrame{}
			}
		}
	}()
	return s
}

func (c *client) stream(t *testing.T, path string) *testStream {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", c.app.srv.URL+BaseURL+path, nil)
		r, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode == 503 && time.Now().Before(until) {
			r.Body.Close()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if r.StatusCode != 200 {
			r.Body.Close()
			t.Fatalf("SSE status=%d", r.StatusCode)
		}
		if r.Header.Get("Content-Type") != "text/event-stream" || r.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("SSE headers=%v", r.Header)
		}
		s := readStream(r.Body)
		t.Cleanup(func() { s.body.Close() })
		var f sseFrame
		select {
		case f = <-s.Frames:
		case <-time.After(3 * time.Second):
			t.Fatal("no immediate SSE handshake")
		}
		if f.Name != "ready" {
			t.Fatalf("first SSE event=%s", f.Name)
		}
		if string(f.Data) != `{"version":1}` {
			t.Fatalf("ready=%s", f.Data)
		}
		return s
	}
}

func (s *testStream) next(t *testing.T, name string, timeout time.Duration) sseFrame {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case f := <-s.Frames:
			if f.Name == name {
				return f
			}
		case <-s.Done:
			t.Fatal("SSE closed before expected frame")
		case <-timer.C:
			t.Fatalf("SSE timeout waiting for %s", name)
		}
	}
}

func (s *testStream) closed(t *testing.T) {
	t.Helper()
	select {
	case <-s.Done:
	case <-time.After(4 * time.Second):
		t.Fatal("SSE access revocation exceeded 4s")
	}
}

func (c *client) userID(t *testing.T) uuid.UUID {
	t.Helper()
	var me struct {
		User struct {
			ID uuid.UUID `json:"id"`
		} `json:"user"`
	}
	c.do("GET", "/me", nil).json(t, &me)
	return me.User.ID
}

func TestSSEEndpointSessionAdminAndIsolation(t *testing.T) {
	a := newTestApp(t)
	anon := a.client()
	for _, path := range []string{"/events", "/admin/events"} {
		if r := anon.do("GET", path, nil); r.status != 401 {
			t.Fatalf("anonymous %s =%d", path, r.status)
		}
	}
	adv := a.newAdvertiser("sse-owner@example.test")
	other := a.newAdvertiser("sse-other@example.test")
	if r := adv.do("GET", "/admin/events", nil); r.status != 403 {
		t.Fatalf("nonadmin=%d", r.status)
	}
	org, otherOrg := adv.orgID(t), other.orgID(t)
	s := adv.stream(t, "/events?organization_id="+otherOrg.String()+"&user_id="+other.userID(t).String())
	// Flush pre-handshake data invalidations, then only a committed own write may arrive.
	time.Sleep(350 * time.Millisecond)
	for len(s.Frames) > 0 {
		<-s.Frames
	}
	_, err := a.db.Pool.Exec(t.Context(), `UPDATE organization SET name='foreign' WHERE id=$1`, otherOrg)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-s.Frames:
		t.Fatalf("foreign event leaked: %s", f.Data)
	case <-time.After(600 * time.Millisecond):
	}
	_, err = a.db.Pool.Exec(t.Context(), `UPDATE organization SET name='own' WHERE id=$1`, org)
	if err != nil {
		t.Fatal(err)
	}
	for {
		f := s.next(t, "invalidate", 3*time.Second)
		var e realtime.Event
		json.Unmarshal(f.Data, &e)
		if e.Topic == "organization.changed" {
			if e.ObjectID == nil || *e.ObjectID != org {
				t.Fatal(e)
			}
			break
		}
	}
	admin := a.adminClient("sse-admin@example.test", accountdomain.AdminSenior)
	as := admin.stream(t, "/admin/events") // No organization was ever created for this user.
	_, err = a.db.Pool.Exec(t.Context(), `SELECT realtime_emit('admin.finance.changed','senior')`)
	if err != nil {
		t.Fatal(err)
	}
	for {
		f := as.next(t, "invalidate", 3*time.Second)
		var e realtime.Event
		json.Unmarshal(f.Data, &e)
		if e.Topic == "admin.finance.changed" {
			break
		}
	}
}

func TestSSEAccessRevocation(t *testing.T) {
	for _, reason := range []string{"logout", "blocked", "role", "cabinet", "membership", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			a := newTestApp(t)
			var c *client
			path := "/events"
			if reason == "role" {
				c = a.adminClient("sse-revoke@example.test", accountdomain.AdminModerator)
				path = "/admin/events"
			} else {
				c = a.newAdvertiser("sse-revoke@example.test")
			}
			uid := c.userID(t)
			s := c.stream(t, path)
			var err error
			switch reason {
			case "logout":
				if r := c.do("POST", "/auth/logout", nil); r.status != 204 {
					t.Fatal(r.status)
				}
			case "blocked":
				_, err = a.db.Pool.Exec(t.Context(), `UPDATE "user" SET status='blocked' WHERE id=$1`, uid)
			case "role":
				_, err = a.db.Pool.Exec(t.Context(), `UPDATE "user" SET admin_role=NULL WHERE id=$1`, uid)
			case "cabinet":
				_, err = a.db.Pool.Exec(t.Context(), `DELETE FROM advertiser_profile WHERE organization_id=$1`, c.orgID(t))
			case "membership":
				_, err = a.db.Pool.Exec(t.Context(), `DELETE FROM organization_member WHERE user_id=$1`, uid)
			case "expiry":
				deadline := a.clk.Now().Add(time.Minute)
				_, err = a.db.Pool.Exec(t.Context(), `UPDATE session SET expires_at=$1 WHERE user_id=$2`, deadline, uid)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(1200 * time.Millisecond)
				var got time.Time
				if err = a.db.Pool.QueryRow(t.Context(), `SELECT expires_at FROM session WHERE user_id=$1`, uid).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if !got.Equal(deadline) {
					t.Fatal("open SSE extended session")
				}
				a.clk.Advance(2 * time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.closed(t)
		})
	}
}

// Keep an actual HTTP server WriteTimeout very short: heartbeats must still work.
func TestSSERefreshesOnlyStreamDeadlineAndHeartbeat(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("sse-heartbeat@example.test")
	shortServer := httptest.NewUnstartedServer(a.srv.Config.Handler)
	shortServer.Config.WriteTimeout = 200 * time.Millisecond
	shortServer.Start()
	t.Cleanup(shortServer.Close)
	a.srv = shortServer
	s := c.stream(t, "/events")
	f := s.next(t, "heartbeat", 18*time.Second)
	if string(f.Data) != `{"version":1}` {
		t.Fatal(string(f.Data))
	}
	if a.srv.Config.WriteTimeout != 200*time.Millisecond {
		t.Fatal("global timeout changed")
	}
}

func TestSSEActualWorkerToTwoAPIReplicas(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("sse-worker@example.test")
	org := adv.orgID(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h, err := apiHandler(ctx, testConfig(), a.mods.in, a.mods, metrics.New())
	if err != nil {
		t.Fatal(err)
	}
	replica := httptest.NewServer(h)
	defer replica.Close()
	defer cancel()
	second := *a
	second.srv = replica
	c2 := &client{app: &second, http: adv.http, csrf: true}
	s1, s2 := adv.stream(t, "/events"), c2.stream(t, "/events")
	a.worker.Register("sse_test_business_write", func(ctx context.Context, _ job.Running) error {
		return a.mods.txRunner.InTx(ctx, func(ctx context.Context) error {
			_, err := a.db.Pool.Exec(ctx, `UPDATE organization SET name='worker updated' WHERE id=$1`, org)
			return err
		})
	}, job.HandlerOptions{})
	if err = a.mods.queue.Enqueue(t.Context(), job.Job{Kind: "sse_test_business_write", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*testStream{s1, s2} {
		for {
			f := s.next(t, "invalidate", 3*time.Second)
			var e realtime.Event
			if json.Unmarshal(f.Data, &e) != nil {
				t.Fatal(string(f.Data))
			}
			if e.Topic == "organization.changed" {
				break
			}
		}
	}
	// A payment modifies financial areas separately on the same live stream.
	a.fund("sse-worker@example.test", 10000)
	for {
		f := s1.next(t, "invalidate", 3*time.Second)
		var e realtime.Event
		json.Unmarshal(f.Data, &e)
		if e.Topic == "balance.changed" {
			break
		}
	}
	s1.body.Close()
	s2.body.Close()
}
