package max_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// postServer — MAX с загрузкой, отправкой, чтением и списком сообщений.
type postServer struct {
	mu          sync.Mutex
	srv         *httptest.Server
	sendBodies  []map[string]any
	notReady    int
	uploadParts []string
	getStatus   int
	meCalls     int
}

func newPostServer(t *testing.T) *postServer {
	t.Helper()
	ps := &postServer{getStatus: http.StatusOK}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/uploads":
			_, _ = w.Write(bytes.ReplaceAll(testdata(t, "upload_slot.json"), []byte("UPLOAD_URL"), []byte(ps.srv.URL+"/upload-target?sig=s3cr3t")))
		case r.Method == http.MethodPost && r.URL.Path == "/upload-target":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, _, err := r.FormFile("data")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(f)
			ps.uploadParts = append(ps.uploadParts, string(b))
			_, _ = w.Write(testdata(t, "upload_done.json"))
		case r.Method == http.MethodPost && r.URL.Path == "/messages":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			ps.sendBodies = append(ps.sendBodies, body)
			if ps.notReady > 0 {
				ps.notReady--
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(testdata(t, "attachment_not_ready.json"))
				return
			}
			_, _ = w.Write(testdata(t, "post_sent.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/messages/mid.ad.1":
			if ps.getStatus != http.StatusOK {
				w.WriteHeader(ps.getStatus)
				_, _ = w.Write([]byte(`{"code":"message.not.found","message":"not found"}`))
				return
			}
			_, _ = w.Write(testdata(t, "post_read.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/messages":
			_, _ = w.Write(testdata(t, "messages.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/me":
			ps.meCalls++
			_, _ = w.Write(testdata(t, "me.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

func TestUploadImage(t *testing.T) {
	ps := newPostServer(t)
	c, d := newClient(t, ps.srv, nil)
	ctx := context.Background()
	token, err := c.UploadImage(ctx, -70000000001, strings.NewReader("JPEGDATA"), 8, "image/jpeg")
	if err != nil || token != "tok.501" {
		t.Fatalf("token %q err %v", token, err)
	}
	if len(ps.uploadParts) != 1 || ps.uploadParts[0] != "JPEGDATA" {
		t.Fatalf("uploaded %v", ps.uploadParts)
	}
	if _, err := c.UploadImage(ctx, -70000000001, strings.NewReader("short"), 8, "image/jpeg"); !job.IsPermanent(err) {
		t.Fatalf("size mismatch: %v", err)
	}
	// Подпись адреса загрузки и токен не сохраняются (07 §4).
	rows, err := d.Pool.Query(ctx, `SELECT request::text || coalesce(response_body::text, '') FROM outbound_request`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		if strings.Contains(s, "s3cr3t") || strings.Contains(s, "tok.501") {
			t.Fatalf("upload secret stored: %s", s)
		}
	}
}

func TestSendChannelPost(t *testing.T) {
	ps := newPostServer(t)
	ps.notReady = 2
	c, d := newClient(t, ps.srv, nil)
	ctx := context.Background()
	text := "<b>Суп</b> за 15 минут\nРеклама. ООО «Ромашка», ИНН 7700000000. erid: 2Vtzqv"
	msg, err := c.SendChannelPost(ctx, -70000000001, text, []string{"t1", "t2"}, max.Recovery{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.ID != "mid.ad.1" || msg.URL != "https://max.ru/c/-70000000001/mid.ad.1" ||
		!msg.At.Equal(time.UnixMilli(1792141207000)) || msg.ContentHash == "" {
		t.Fatalf("posted %+v", msg)
	}
	// attachment.not.ready дважды, затем успех — три запроса одного вызова (05 §2.6 шаг 4).
	if len(ps.sendBodies) != 3 {
		t.Fatalf("send requests %d", len(ps.sendBodies))
	}
	b := ps.sendBodies[0]
	atts, _ := b["attachments"].([]any)
	if b["format"] != "html" || b["disable_link_preview"] != true || b["text"] != text || len(atts) != 2 {
		t.Fatalf("request body %v", b)
	}
	first, _ := atts[0].(map[string]any)
	if first["type"] != "image" || first["payload"].(map[string]any)["token"] != "t1" {
		t.Fatalf("attachment %v", first)
	}

	// Хеш ответа отправки равен хешу чтения того же сообщения (R-6).
	read, err := c.GetMessage(ctx, "mid.ad.1")
	if err != nil {
		t.Fatal(err)
	}
	if read.ContentHash != msg.ContentHash || read.SenderID != 1 || read.Views == nil || *read.Views != 4200 {
		t.Fatalf("read %+v, sent hash %s", read, msg.ContentHash)
	}

	// Текст поста не сохраняется в outbound_request (07 §5.4).
	rows, err := d.Pool.Query(ctx, `SELECT request::text || coalesce(response_body::text, '') FROM outbound_request`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		if strings.Contains(s, "Ромашка") || strings.Contains(s, "Суп") {
			t.Fatalf("post text stored: %s", s)
		}
	}
}

func TestSendChannelPostAttachmentNeverReady(t *testing.T) {
	ps := newPostServer(t)
	ps.notReady = 100
	c, _ := newClient(t, ps.srv, nil)
	_, err := c.SendChannelPost(context.Background(), -70000000001, "text", []string{"t1"}, max.Recovery{})
	if e, ok := max.AsAPIError(err); !ok || e.Code != "attachment.not.ready" {
		t.Fatalf("err %v", err)
	}
	if len(ps.sendBodies) != 7 {
		t.Fatalf("send requests %d, want 1 + 6 retries", len(ps.sendBodies))
	}
}

func TestGetMessageNotFound(t *testing.T) {
	ps := newPostServer(t)
	ps.getStatus = http.StatusNotFound
	c, _ := newClient(t, ps.srv, nil)
	if _, err := c.GetMessage(context.Background(), "mid.ad.1"); !errors.Is(err, max.ErrMessageNotFound) {
		t.Fatalf("err %v", err)
	}
}

// Sanitized GET of our dedicated test post on 2026-11-03: MAX omits
// sender for a bot publication made on behalf of the channel.
func TestSenderlessChannelPublication(t *testing.T) {
	body := testdata(t, "post_channel_senderless.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/messages" {
			_, _ = w.Write(append(append([]byte(`{"messages":[`), body...), []byte(`]}`)...))
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	ctx := context.Background()
	m, err := c.GetMessage(ctx, "mid.check.senderless")
	if err != nil || m.SenderID != 0 || m.FromBot || m.Views == nil || m.ContentHash == "" {
		t.Fatalf("senderless post: %+v, err %v", m, err)
	}
	found, err := c.FindPost(ctx, -70000000001, m.At.Add(-time.Minute), m.At.Add(time.Minute), 0,
		"maxim-check:00000000-0000-4000-8000-000000000001")
	if err != nil || found == nil || found.MID != m.MID || found.ContentHash != m.ContentHash {
		t.Fatalf("channel marker lookup: %+v, err %v", found, err)
	}
}

func TestFindPostAndBotUserID(t *testing.T) {
	ps := newPostServer(t)
	c, _ := newClient(t, ps.srv, nil)
	ctx := context.Background()
	to := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	from := to.Add(-30 * 24 * time.Hour)
	m, err := c.FindPost(ctx, -70000000001, from, to, 1, "ИНН 7700000000")
	if err != nil || m == nil || m.MID != "mid.3" || m.SenderID != 1 {
		t.Fatalf("found %+v err %v", m, err)
	}
	// Подстрока есть, но автор не наш бот.
	if m, err := c.FindPost(ctx, -70000000001, from, to, 1, "Пирог"); err != nil || m != nil {
		t.Fatalf("foreign post found %+v err %v", m, err)
	}
	for range 2 {
		id, err := c.BotUserID(ctx)
		if err != nil || id == 0 {
			t.Fatalf("bot id %d err %v", id, err)
		}
	}
	if ps.meCalls != 1 {
		t.Fatalf("GET /me called %d times", ps.meCalls)
	}
}

func TestParsePublicationUpdates(t *testing.T) {
	edited, err := max.ParseUpdate(testdata(t, "updates/message_edited.json"))
	if err != nil || edited.MessageID() != "mid.ad.1" || edited.ChatID != -70000000001 || edited.ContentHash() == "" ||
		!edited.MessageAt().Equal(time.UnixMilli(1792141207000)) || edited.SenderID() != 42 {
		t.Fatalf("edited %+v err %v", edited, err)
	}
	removed, err := max.ParseUpdate(testdata(t, "updates/message_removed.json"))
	if err != nil || removed.MessageID() != "mid.ad.1" || removed.ChatID != -70000000001 || removed.RemovedBy != 42 ||
		!removed.MessageAt().Equal(time.UnixMilli(1792145000000)) {
		t.Fatalf("removed %+v err %v", removed, err)
	}
	for _, kind := range []string{max.UpdateMessageEdited, max.UpdateMessageRemoved} {
		found := false
		for _, k := range max.ChannelUpdateTypes {
			found = found || k == kind
		}
		if !found {
			t.Fatalf("%s not subscribed", kind)
		}
	}
}
