package httpapi

import (
	"context"
	"encoding/base64"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/support/app"
	"github.com/leha-creator/chanly/internal/support/domain"
	"log/slog"
	"strconv"
)

type Handlers struct {
	Svc *app.Service
	Log *slog.Logger
}

func New(s *app.Service, log *slog.Logger) *Handlers { return &Handlers{s, log} }
func (h *Handlers) admin(ctx context.Context) (context.Context, *httpx.Principal, error) {
	return httpx.AdminContext(ctx, h.Log, "Support.Admin", httpx.AdminAny...)
}

func (h *Handlers) ListSupportConversations(ctx context.Context, req apigen.ListSupportConversationsRequestObject) (apigen.ListSupportConversationsResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	pg, e := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if e != nil {
		return nil, e
	}
	f := domain.Filter{ActorID: p.UserID, Query: value(req.Params.Query), Status: string(value(req.Params.Status)), Mine: value(req.Params.Mine), NeedsReply: value(req.Params.NeedsReply), Offset: int32(pg.Offset), Limit: int32(pg.Limit)}
	items, total, e := h.Svc.List(ctx, f)
	if e != nil {
		return nil, e
	}
	out := apigen.SupportConversationList{Enabled: h.Svc.Enabled, Items: []apigen.SupportConversation{}, Total: total, NextCursor: pg.Next(total)}
	l := h.Svc.Limits
	out.Limits = apigen.SupportLimits{Image: l.Image, Video: l.Video, Audio: l.Audio, File: l.File, MaxAttachments: l.MaxAttachments, VoiceSeconds: l.VoiceSeconds}
	for _, c := range items {
		out.Items = append(out.Items, conversation(c))
	}
	return apigen.ListSupportConversations200JSONResponse(out), nil
}
func (h *Handlers) GetSupportConversation(ctx context.Context, req apigen.GetSupportConversationRequestObject) (apigen.GetSupportConversationResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	c, e := h.Svc.Conversation(ctx, req.Id, p.UserID)
	return apigen.GetSupportConversation200JSONResponse(conversation(c)), e
}
func (h *Handlers) UpdateSupportConversation(ctx context.Context, req apigen.UpdateSupportConversationRequestObject) (apigen.UpdateSupportConversationResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	if req.Body == nil {
		return nil, apperr.Validation("body", "required")
	}
	c, e := h.Svc.Update(ctx, req.Id, p.UserID, req.Body.Version, string(req.Body.Status), req.Body.AssigneeId)
	return apigen.UpdateSupportConversation200JSONResponse(conversation(c)), e
}
func (h *Handlers) ListSupportMessages(ctx context.Context, req apigen.ListSupportMessagesRequestObject) (apigen.ListSupportMessagesResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	var before int64
	if req.Params.Cursor != nil {
		b, e := base64.RawURLEncoding.DecodeString(*req.Params.Cursor)
		if e != nil {
			return nil, apperr.Validation("cursor", "invalid")
		}
		before, e = strconv.ParseInt(string(b), 10, 64)
		if e != nil || before <= 0 {
			return nil, apperr.Validation("cursor", "invalid")
		}
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	rows, e := h.Svc.Messages(ctx, req.Id, p.UserID, before, int32(limit))
	if e != nil {
		return nil, e
	}
	out := apigen.SupportMessageList{Items: []apigen.SupportMessage{}}
	for _, m := range rows {
		out.Items = append(out.Items, message(m))
	}
	if len(rows) == limit {
		c := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(rows[len(rows)-1].Sequence, 10)))
		out.NextCursor = &c
	}
	return apigen.ListSupportMessages200JSONResponse(out), nil
}
func (h *Handlers) SendSupportMessage(ctx context.Context, req apigen.SendSupportMessageRequestObject) (apigen.SendSupportMessageResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	if req.Body == nil {
		return nil, apperr.Validation("body", "required")
	}
	b := req.Body
	m, e := h.Svc.Reply(ctx, req.Id, p.UserID, domain.Reply{Text: b.Text, Attachments: b.Attachments, ClientKey: b.ClientKey, ReplyToSequence: b.ReplyToSequence})
	return apigen.SendSupportMessage202JSONResponse(message(m)), e
}
func (h *Handlers) ReadSupportConversation(ctx context.Context, req apigen.ReadSupportConversationRequestObject) (apigen.ReadSupportConversationResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	if req.Body == nil {
		return nil, apperr.Validation("body", "required")
	}
	e = h.Svc.Read(ctx, req.Id, p.UserID, req.Body.Sequence)
	return apigen.ReadSupportConversation204Response{}, e
}
func (h *Handlers) RetrySupportMessage(ctx context.Context, req apigen.RetrySupportMessageRequestObject) (apigen.RetrySupportMessageResponseObject, error) {
	ctx, _, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	m, e := h.Svc.Retry(ctx, req.Id)
	return apigen.RetrySupportMessage202JSONResponse(message(m)), e
}
func (h *Handlers) GetSupportAttachmentMetadata(ctx context.Context, req apigen.GetSupportAttachmentMetadataRequestObject) (apigen.GetSupportAttachmentMetadataResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	a, e := h.Svc.Attachment(ctx, req.Id, p.UserID)
	return apigen.GetSupportAttachmentMetadata200JSONResponse(attachment(a)), e
}
