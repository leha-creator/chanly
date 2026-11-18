package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (h *Handlers) ListSupportConversations(ctx context.Context, r api.ListSupportConversationsRequestObject) (api.ListSupportConversationsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	s := tx.D.Support
	items := []api.SupportConversation{}
	for id, c := range s.Conversations {
		if r.Params.Status != nil && string(c.Status) != string(*r.Params.Status) {
			continue
		}
		if r.Params.NeedsReply != nil && *r.Params.NeedsReply && !(c.Status == "open" && c.LastIncoming > c.AnsweredThrough) {
			continue
		}
		if r.Params.Mine != nil && *r.Params.Mine && (c.AssigneeId == nil || *c.AssigneeId != u.ID) {
			continue
		}
		if r.Params.Query != nil && !strings.Contains(strings.ToLower(c.Name+" "+c.Username+" "+c.MaxUserId), strings.ToLower(*r.Params.Query)) {
			continue
		}
		items = append(items, s.ForActor(id, u.ID))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	pg, next, total := page(items, r.Params.Limit, r.Params.Cursor)
	return api.ListSupportConversations200JSONResponse{Items: pg, Total: *total, NextCursor: next, Enabled: true, Limits: api.SupportLimits{Image: 20 << 20, Video: 250 << 20, Audio: 50 << 20, File: 20 << 20, MaxAttachments: 5, VoiceSeconds: 600}}, nil
}
func (h *Handlers) GetSupportConversation(ctx context.Context, r api.GetSupportConversationRequestObject) (api.GetSupportConversationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	if _, ok := tx.D.Support.Conversations[r.Id]; !ok {
		return nil, store.NotFound("support", nil)
	}
	return api.GetSupportConversation200JSONResponse(tx.D.Support.ForActor(r.Id, u.ID)), nil
}
func (h *Handlers) UpdateSupportConversation(ctx context.Context, r api.UpdateSupportConversationRequestObject) (api.UpdateSupportConversationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	c, ok := tx.D.Support.Conversations[r.Id]
	if !ok {
		return nil, store.NotFound("support", nil)
	}
	if c.Version != r.Body.Version {
		return nil, store.Conflict("conflict", "conversation changed", nil)
	}
	if r.Body.AssigneeId != nil {
		a := tx.D.Users[*r.Body.AssigneeId]
		if a == nil || a.AdminRole == "" || a.Blocked {
			return nil, store.Invalid("assignee_id", "admin_required", "administrator required")
		}
	}
	c.Status = api.SupportConversationStatus(r.Body.Status)
	c.AssigneeId = r.Body.AssigneeId
	c.Version++
	c.UpdatedAt = tx.Now
	tx.D.Support.Conversations[r.Id] = c
	return api.UpdateSupportConversation200JSONResponse(tx.D.Support.ForActor(r.Id, u.ID)), nil
}
func (h *Handlers) ListSupportMessages(ctx context.Context, r api.ListSupportMessagesRequestObject) (api.ListSupportMessagesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, e := admin(ctx, tx); e != nil {
		return nil, e
	}
	if _, ok := tx.D.Support.Conversations[r.Id]; !ok {
		return nil, store.NotFound("support", nil)
	}
	before := int64(0)
	if r.Params.Cursor != nil {
		b, e := base64.RawURLEncoding.DecodeString(*r.Params.Cursor)
		if e != nil {
			return nil, store.Invalid("cursor", "invalid", "invalid cursor")
		}
		before, e = strconv.ParseInt(string(b), 10, 64)
		if e != nil {
			return nil, store.Invalid("cursor", "invalid", "invalid cursor")
		}
	}
	items := []api.SupportMessage{}
	all := tx.D.Support.Messages[r.Id]
	limit := 20
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	for i := len(all) - 1; i >= 0; i-- {
		if before == 0 || all[i].Sequence < before {
			items = append(items, all[i])
			if len(items) == limit {
				break
			}
		}
	}
	out := api.ListSupportMessages200JSONResponse{Items: items}
	if len(items) == limit {
		n := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(items[len(items)-1].Sequence, 10)))
		out.NextCursor = &n
	}
	return out, nil
}
func (h *Handlers) ReadSupportConversation(ctx context.Context, r api.ReadSupportConversationRequestObject) (api.ReadSupportConversationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	s := tx.D.Support
	c, ok := s.Conversations[r.Id]
	if !ok {
		return nil, store.NotFound("support", nil)
	}
	if s.Reads[r.Id] == nil {
		s.Reads[r.Id] = map[uuid.UUID]int64{}
	}
	s.Reads[r.Id][u.ID] = max(s.Reads[r.Id][u.ID], min(r.Body.Sequence, c.LastSequence))
	return api.ReadSupportConversation204Response{}, nil
}
func (h *Handlers) SendSupportMessage(ctx context.Context, r api.SendSupportMessageRequestObject) (api.SendSupportMessageResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	s := tx.D.Support
	c, ok := s.Conversations[r.Id]
	if !ok {
		return nil, store.NotFound("support", nil)
	}
	b, _ := json.Marshal(r.Body)
	if old, ok := s.Keys[r.Id][r.Body.ClientKey]; ok {
		if old.Request != string(b) {
			return nil, store.Conflict("conflict", "key reused", nil)
		}
		return api.SendSupportMessage202JSONResponse(old.Message), nil
	}
	if c.Status != "open" {
		return nil, store.Conflict("conflict", "conversation closed", nil)
	}
	if r.Body.Text == "" && len(r.Body.Attachments) == 0 {
		return nil, store.Invalid("text", "required", "message required")
	}
	attachments := []api.SupportAttachment{}
	for _, id := range r.Body.Attachments {
		a, ok := s.Attachments[id]
		if !ok || s.Owners[id] != u.ID {
			return nil, store.Invalid("attachments", "not_available", "attachment unavailable")
		}
		attachments = append(attachments, a)
	}
	c.LastSequence++
	c.Version++
	c.UpdatedAt = tx.Now
	c.AnsweredThrough = max(c.AnsweredThrough, r.Body.ReplyToSequence)
	c.NeedsReply = c.LastIncoming > c.AnsweredThrough
	m := api.SupportMessage{Id: store.NewID(), ConversationId: c.Id, Sequence: c.LastSequence, Direction: "outbound", Text: r.Body.Text, ActorId: &u.ID, Status: "sent", ExternalId: "mock-only", ReplyToSequence: r.Body.ReplyToSequence, CreatedAt: tx.Now, Attachments: attachments}
	s.Conversations[c.Id] = c
	s.Messages[c.Id] = append(s.Messages[c.Id], m)
	if s.Keys[c.Id] == nil {
		s.Keys[c.Id] = map[uuid.UUID]store.SupportKey{}
	}
	s.Keys[c.Id][r.Body.ClientKey] = store.SupportKey{Request: string(b), Message: m}
	return api.SendSupportMessage202JSONResponse(m), nil
}
func (h *Handlers) RetrySupportMessage(ctx context.Context, r api.RetrySupportMessageRequestObject) (api.RetrySupportMessageResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, e := admin(ctx, tx); e != nil {
		return nil, e
	}
	for cid, list := range tx.D.Support.Messages {
		for i, m := range list {
			if m.Id == r.Id {
				if m.Status != "failed" {
					return nil, store.Conflict("conflict", "only failed messages can retry", nil)
				}
				m.Status = "sent"
				m.Reason = ""
				m.ExternalId = "mock-only"
				tx.D.Support.Messages[cid][i] = m
				return api.RetrySupportMessage202JSONResponse(m), nil
			}
		}
	}
	return nil, store.NotFound("support message", nil)
}
func (h *Handlers) GetSupportAttachmentMetadata(ctx context.Context, r api.GetSupportAttachmentMetadataRequestObject) (api.GetSupportAttachmentMetadataResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	a, ok := tx.D.Support.Attachments[r.Id]
	if !ok {
		return nil, store.NotFound("support attachment", nil)
	}
	if owner, exists := tx.D.Support.Owners[r.Id]; exists && owner != u.ID {
		return nil, store.NotFound("support attachment", nil)
	}
	return api.GetSupportAttachmentMetadata200JSONResponse(a), nil
}
func (h *Handlers) UploadSupportAttachment(ctx context.Context, r api.UploadSupportAttachmentRequestObject) (api.UploadSupportAttachmentResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	kind, name := "", ""
	var body []byte
	for {
		p, e := r.Body.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, store.Invalid("file", "malformed", "malformed file")
		}
		b, e := io.ReadAll(io.LimitReader(p, (250<<20)+1))
		if e != nil || len(b) > 250<<20 {
			return nil, store.Invalid("file", "too_large", "file too large")
		}
		if p.FormName() == "type" {
			kind = string(b)
		} else if p.FormName() == "file" {
			body = b
			name = p.FileName()
		}
		p.Close()
	}
	a := api.SupportAttachment{Id: store.NewID(), Type: api.SupportAttachmentType(kind), Name: name, Size: int64(len(body)), Mime: http.DetectContentType(body), Status: "ready"}
	if kind == "audio" && strings.HasPrefix(a.Mime, "video/") {
		a.Mime = "audio/webm";if strings.HasSuffix(strings.ToLower(name),".m4a"){a.Mime="audio/mp4"}
	}
	tx.D.Support.Attachments[a.Id] = a
	tx.D.Support.Files[a.Id] = body
	tx.D.Support.Owners[a.Id] = u.ID
	return api.UploadSupportAttachment201JSONResponse(a), nil
}

type supportFile struct {
	data                   []byte
	mime, name, rangeValue string
}

func (f supportFile) VisitGetSupportAttachmentResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", f.mime)
	w.Header().Set("Cache-Control", "private, no-store")
	r, _ := http.NewRequest("GET", "http://mock.local/", nil)
	if f.rangeValue != "" {
		r.Header.Set("Range", f.rangeValue)
	}
	http.ServeContent(w, r, f.name, time.Time{}, bytes.NewReader(f.data))
	return nil
}
func (h *Handlers) GetSupportAttachment(ctx context.Context, r api.GetSupportAttachmentRequestObject) (api.GetSupportAttachmentResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, e := admin(ctx, tx)
	if e != nil {
		return nil, e
	}
	s := tx.D.Support
	a, ok := s.Attachments[r.Id]
	if !ok {
		return nil, store.NotFound("support attachment", nil)
	}
	if owner, exists := s.Owners[r.Id]; exists && owner != u.ID {
		return nil, store.NotFound("support attachment", nil)
	}
	rangeValue := ""
	if r.Params.Range != nil {
		rangeValue = *r.Params.Range
	}
	return supportFile{s.Files[r.Id], a.Mime, a.Name, rangeValue}, nil
}
func (h *Handlers) SupportMaxWebhook(ctx context.Context, r api.SupportMaxWebhookRequestObject) (api.SupportMaxWebhookResponseObject, error) {
	return api.SupportMaxWebhook200Response{}, nil
}
