package httpapi

import (
	"bytes"
	"context"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/support/app"
	"github.com/leha-creator/chanly/internal/support/domain"
	"net/http/httptest"
	"testing"
)

type attachmentRepo struct {
	app.Repository
	a domain.Attachment
}

func (r attachmentRepo) Attachment(context.Context, uuid.UUID) (domain.Attachment, error) {
	return r.a, nil
}
func TestPrivateAttachmentRangeAndRoles(t *testing.T) {
	ctx := context.Background()
	blob := storagefake.New()
	data := []byte("0123456789")
	if e := blob.PutPrivate(ctx, "support/test", bytes.NewReader(data), int64(len(data)), "audio/mpeg"); e != nil {
		t.Fatal(e)
	}
	mid := ids.New()
	a := domain.Attachment{ID: ids.New(), MessageID: &mid, Type: "audio", Status: "ready", Size: 10, MIME: "audio/mpeg", StorageKey: "support/test"}
	s := &app.Service{Repo: attachmentRepo{a: a}, Storage: blob}
	h := New(s, logx.Discard())
	rangeHeader := "bytes=2-5"
	req := apigen.GetSupportAttachmentRequestObject{Id: a.ID, Params: apigen.GetSupportAttachmentParams{Range: &rangeHeader}}
	for _, role := range []string{"moderator", "senior"} {
		p := httpx.WithPrincipal(ctx, &httpx.Principal{UserID: ids.New(), AdminRole: role})
		response, e := h.GetSupportAttachment(p, req)
		if e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		if e = response.VisitGetSupportAttachmentResponse(w); e != nil {
			t.Fatal(e)
		}
		if w.Code != 206 || w.Body.String() != "2345" {
			t.Fatalf("range: %d %q", w.Code, w.Body.String())
		}
	}
	if _, e := h.GetSupportAttachment(httpx.WithPrincipal(ctx, &httpx.Principal{UserID: ids.New()}), req); e == nil {
		t.Fatal("ordinary user could download support file")
	}
}
