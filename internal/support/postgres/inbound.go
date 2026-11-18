package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/support/domain"
	"github.com/leha-creator/chanly/internal/support/postgres/sqlc"
	"time"
)

func (s *Store) Ingest(ctx context.Context, in domain.Incoming, now time.Time) (domain.Message, bool, error) {
	q := s.q(ctx)
	cid, e := q.SupportCreateConversation(ctx, sqlc.SupportCreateConversationParams{ID: ids.New(), BotID: in.BotID, MaxUserID: in.UserID, Name: in.Name, Username: in.Username, CreatedAt: now})
	if e != nil {
		return domain.Message{}, false, e
	}
	if _, e = q.SupportLock(ctx, cid); e != nil {
		return domain.Message{}, false, e
	}
	old, e := q.SupportIncomingExists(ctx, sqlc.SupportIncomingExistsParams{BotID: in.BotID, ExternalID: in.ExternalID})
	if e == nil {
		m, e := s.Message(ctx, old, false)
		return m, false, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return domain.Message{}, false, e
	}
	seq, e := q.SupportNextSequence(ctx, sqlc.SupportNextSequenceParams{ID: cid, UpdatedAt: now})
	if e != nil {
		return domain.Message{}, false, e
	}
	id := ids.New()
	e = q.SupportInsertMessage(ctx, sqlc.SupportInsertMessageParams{ID: id, ConversationID: cid, Sequence: seq, Direction: "inbound", Text: in.Text, Status: domain.Sent, BotID: in.BotID, ExternalID: in.ExternalID, EventAt: in.At, CreatedAt: now})
	if e != nil {
		return domain.Message{}, false, e
	}
	if e = q.SupportSetIncoming(ctx, sqlc.SupportSetIncomingParams{ID: cid, LastIncoming: seq, UpdatedAt: now}); e != nil {
		return domain.Message{}, false, e
	}
	for _, a := range in.Media {
		status := "pending"
		kind := a.Type
		if kind != "image" && kind != "video" && kind != "audio" && kind != "file" {
			status = "unsupported"
			kind = "unsupported"
		}
		if e = s.SaveAttachment(ctx, domain.Attachment{ID: ids.New(), MessageID: &id, Type: kind, Name: a.Name, Size: a.Size, Status: status, ProviderToken: a.Token, ProviderURL: a.URL}); e != nil {
			return domain.Message{}, false, e
		}
	}
	if e = q.SupportApplyRevision(ctx, id); e != nil {
		return domain.Message{}, false, e
	}
	m, e := s.Message(ctx, id, false)
	return m, true, e
}

func (s *Store) Revise(ctx context.Context, in domain.Incoming) error {
	q := s.q(ctx)
	if e := q.SupportRevise(ctx, sqlc.SupportReviseParams{BotID: in.BotID, ExternalID: in.ExternalID, Text: in.Text, Deleted: in.Type == "message_removed", EventAt: in.At}); e != nil {
		return e
	}
	id, e := q.SupportIncomingExists(ctx, sqlc.SupportIncomingExistsParams{BotID: in.BotID, ExternalID: in.ExternalID})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	return q.SupportApplyRevision(ctx, id)
}
func (s *Store) AttachmentIDs(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	return s.q(ctx).SupportMessageAttachments(ctx, &id)
}
