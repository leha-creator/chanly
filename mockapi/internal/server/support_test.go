package server_test

import (
	"bytes"
	"context"
	"github.com/google/uuid"
	"maxim/mockapi/internal/api"
	"mime/multipart"
	"testing"
)

const supportID = "019a9000-0000-7000-8000-000000001201"

func init() {
	cases["listSupportConversations"] = opCase{user: adm}
	cases["getSupportConversation"] = opCase{user: adm, params: map[string]string{"id": supportID}}
	cases["listSupportMessages"] = opCase{user: adm, params: map[string]string{"id": supportID}}
	cases["readSupportConversation"] = opCase{user: adm, params: map[string]string{"id": supportID}, body: map[string]any{"sequence": 1}}
	cases["updateSupportConversation"] = opCase{user: adm, params: map[string]string{"id": supportID}, body: map[string]any{"version": 1, "status": "closed"}}
	cases["sendSupportMessage"] = opCase{user: adm, params: map[string]string{"id": supportID}, body: map[string]any{"text": "Проверим подключение.", "attachments": []string{}, "client_key": "019a9000-0000-7000-8000-000000001900", "reply_to_sequence": 1}}
	cases["retrySupportMessage"] = opCase{user: adm, prepare: func(e *env) (map[string]string, any) {
		tx, done := e.st.Begin(context.Background())
		defer done()
		for _, m := range tx.D.Support.Messages[uuid.MustParse(supportID)] {
			if m.Status == "failed" {
				return id(m.Id), noBodyMarker
			}
		}
		e.t.Fatal("failed fixture missing")
		return nil, nil
	}}
	file := func(e *env) (map[string]string, any) {
		tx, done := e.st.Begin(context.Background())
		defer done()
		for aid := range tx.D.Support.Attachments {
			return id(aid), noBodyMarker
		}
		e.t.Fatal("attachment fixture missing")
		return nil, nil
	}
	cases["getSupportAttachment"] = opCase{user: adm, prepare: file}
	cases["getSupportAttachmentMetadata"] = opCase{user: adm, prepare: file}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("type", "image")
	p, _ := w.CreateFormFile("file", "test.png")
	_, _ = p.Write(pngBytes())
	_ = w.Close()
	cases["uploadSupportAttachment"] = opCase{user: adm, body: b.Bytes(), header: map[string]string{"Content-Type": w.FormDataContentType()}}
	cases["supportMaxWebhook"] = opCase{params: map[string]string{"secret": "test-support"}, body: map[string]any{"update_type": "bot_started"}}
}
func TestSupportReplyIdempotencyAndAccess(t *testing.T) {
	e := newEnv(t)
	c := e.client(mod)
	base := "/admin/support/conversations/" + supportID
	body := map[string]any{"text": "Поможем с подключением.", "attachments": []string{}, "client_key": "019a9000-0000-7000-8000-000000001999", "reply_to_sequence": 1}
	var first, again api.SupportMessage
	e.do(c, "POST", base+"/messages", body, "").json(t, &first)
	e.do(c, "POST", base+"/messages", body, "").json(t, &again)
	if first.Id != again.Id {
		t.Fatal("HTTP retry duplicated reply")
	}
	body["text"] = "Изменённый ответ"
	if r := e.do(c, "POST", base+"/messages", body, ""); r.status != 409 {
		t.Fatalf("reused key: %d", r.status)
	}
	if r := e.do(e.client(adv), "GET", "/admin/support/conversations", nil, ""); r.status != 403 {
		t.Fatal("ordinary user read support")
	}
	var card api.SupportConversation
	e.do(c, "GET", base, nil, "").json(t, &card)
	if card.NeedsReply {
		t.Fatal("confirmed reply did not cover incoming")
	}
}
