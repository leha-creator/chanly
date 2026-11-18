package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/support/domain"
	"github.com/leha-creator/chanly/internal/support/postgres/sqlc"
)

type Store struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Store { return &Store{pool, log} }
func (s *Store) q(ctx context.Context) *sqlc.Queries  { return sqlc.New(db.Conn(ctx, s.Pool)) }
func decode[T any](raw []byte, err error) (T, error) {
	var v T
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperr.NotFound("support object not found")
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}
func decodeMany[T any](raw [][]byte, err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raw))
	for _, b := range raw {
		v, e := decode[T](b, nil)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Store) List(ctx context.Context, f domain.Filter) ([]domain.Conversation, int, error) {
	q := s.q(ctx)
	rows, err := q.SupportList(ctx, sqlc.SupportListParams{ActorID: f.ActorID, Status: f.Status, Mine: f.Mine, NeedsReply: f.NeedsReply, Query: f.Query, PageLimit: f.Limit, PageOffset: f.Offset})
	items, err := decodeMany[domain.Conversation](rows, err)
	if err != nil {
		return nil, 0, err
	}
	n, err := q.SupportTotal(ctx, sqlc.SupportTotalParams{Status: f.Status, Mine: f.Mine, ActorID: f.ActorID, NeedsReply: f.NeedsReply, Query: f.Query})
	return items, int(n), err
}
func (s *Store) Conversation(ctx context.Context, id, actor uuid.UUID, lock bool) (domain.Conversation, error) {
	q := s.q(ctx)
	if lock {
		if _, err := q.SupportLock(ctx, id); err != nil {
			return domain.Conversation{}, err
		}
	}
	b, e := q.SupportGet(ctx, sqlc.SupportGetParams{ID: id, ActorID: actor})
	return decode[domain.Conversation](b, e)
}
func (s *Store) Messages(ctx context.Context, id uuid.UUID, before int64, limit int32) ([]domain.Message, error) {
	b, e := s.q(ctx).SupportHistory(ctx, sqlc.SupportHistoryParams{ConversationID: id, BeforeSequence: before, PageLimit: limit})
	return decodeMany[domain.Message](b, e)
}
func (s *Store) Message(ctx context.Context, id uuid.UUID, lock bool) (domain.Message, error) {
	b, e := s.q(ctx).SupportMessage(ctx, id)
	m, e := decode[domain.Message](b, e)
	if e == nil && lock {
		_, e = s.q(ctx).SupportLock(ctx, m.ConversationID)
	}
	return m, e
}
func (s *Store) Attachment(ctx context.Context, id uuid.UUID) (domain.Attachment, error) {
	r, e := s.q(ctx).SupportAttachment(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Attachment{}, apperr.NotFound("attachment not found")
	}
	return domain.Attachment{ID: r.ID, MessageID: r.MessageID, ActorID: r.ActorID, Type: r.Type, MIME: r.Mime, Size: r.Size, Name: r.Name, Status: r.Status, Duration: r.Duration, Width: int(r.Width), Height: int(r.Height), StorageKey: r.StorageKey, OriginalKey: r.OriginalKey, ProviderToken: r.ProviderToken, ProviderURL: r.ProviderUrl}, e
}
func (s *Store) SaveAttachment(ctx context.Context, a domain.Attachment) error {
	return s.q(ctx).SupportSaveAttachment(ctx, sqlc.SupportSaveAttachmentParams{ID: a.ID, MessageID: a.MessageID, ActorID: a.ActorID, Type: a.Type, Mime: a.MIME, Size: a.Size, Name: a.Name, Status: a.Status, Duration: a.Duration, Width: int32(a.Width), Height: int32(a.Height), StorageKey: a.StorageKey, OriginalKey: a.OriginalKey, ProviderToken: a.ProviderToken, ProviderUrl: a.ProviderURL})
}

func (s *Store) ClaimMedia(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	n, e := s.q(ctx).SupportClaimMedia(ctx, sqlc.SupportClaimMediaParams{ID: id, ProcessingAt: &now, ProcessingAt_2: timePointer(now.Add(-10 * time.Minute))})
	return n == 1, e
}
func timePointer(t time.Time) *time.Time { return &t }

func (s *Store) QueueReply(ctx context.Context, id, actor uuid.UUID, r domain.Reply, now time.Time) (domain.Message, bool, error) {
	c, err := s.Conversation(ctx, id, actor, true)
	if err != nil {
		return domain.Message{}, false, err
	}
	q := s.q(ctx)
	data, _ := json.Marshal(r)
	hash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(hash[:])
	old, err := q.SupportExistingReply(ctx, sqlc.SupportExistingReplyParams{ConversationID: id, ClientKey: &r.ClientKey})
	if err == nil {
		if old.RequestHash != fingerprint {
			return domain.Message{}, false, apperr.Conflict("idempotency key reused")
		}
		m, e := s.Message(ctx, old.ID, false)
		return m, false, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, false, err
	}
	if c.Status != "open" {
		return domain.Message{}, false, apperr.Conflict("conversation is closed")
	}
	if r.ReplyToSequence > c.LastSequence {
		return domain.Message{}, false, apperr.Validation("reply_to_sequence", "out_of_range")
	}
	sequence, err := q.SupportNextSequence(ctx, sqlc.SupportNextSequenceParams{ID: id, UpdatedAt: now})
	if err != nil {
		return domain.Message{}, false, err
	}
	mid := ids.New()
	err = q.SupportInsertMessage(ctx, sqlc.SupportInsertMessageParams{ID: mid, ConversationID: id, Sequence: sequence, Direction: "outbound", Text: r.Text, ActorID: &actor, Status: domain.Queued, BotID: c.BotID, ClientKey: &r.ClientKey, RequestHash: fingerprint, ReplyToSequence: r.ReplyToSequence, EventAt: now, CreatedAt: now})
	if err != nil {
		return domain.Message{}, false, err
	}
	for _, aid := range r.Attachments {
		n, e := q.SupportBindAttachment(ctx, sqlc.SupportBindAttachmentParams{ID: aid, MessageID: &mid, ActorID: &actor})
		if e != nil {
			return domain.Message{}, false, e
		}
		if n != 1 {
			a, e := s.Attachment(ctx, aid)
			if e != nil || a.MessageID == nil {
				return domain.Message{}, false, apperr.Validation("attachments", "not_available")
			}
			original, e := s.Message(ctx, *a.MessageID, false)
			if e != nil || original.ConversationID != id || original.Direction != "outbound" || original.Status != domain.Unknown {
				return domain.Message{}, false, apperr.Validation("attachments", "not_available")
			}
			a.ID = ids.New()
			a.MessageID = &mid
			a.ActorID = &actor
			if e = s.SaveAttachment(ctx, a); e != nil {
				return domain.Message{}, false, e
			}
		}
	}
	m, e := s.Message(ctx, mid, false)
	s.Log.DebugContext(ctx, "support reply persisted", "message_id", mid, "conversation_id", id)
	return m, true, e
}
func (s *Store) MarkRead(ctx context.Context, id, actor uuid.UUID, seq int64) error {
	return s.q(ctx).SupportMarkRead(ctx, sqlc.SupportMarkReadParams{ConversationID: id, ActorID: actor, Sequence: seq})
}
func (s *Store) Update(ctx context.Context, id uuid.UUID, version int64, status string, assignee *uuid.UUID, now time.Time) error {
	q := s.q(ctx)
	if assignee != nil {
		ok, e := q.SupportAdminExists(ctx, *assignee)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.Validation("assignee_id", "admin_required")
		}
	}
	n, e := q.SupportUpdate(ctx, sqlc.SupportUpdateParams{ID: id, Version: version, Status: status, AssigneeID: assignee, UpdatedAt: now})
	if e != nil {
		return e
	}
	if n != 1 {
		return apperr.Conflict("conversation changed")
	}
	return nil
}
func (s *Store) Claim(ctx context.Context, id uuid.UUID, now time.Time) (domain.Message, bool, error) {
	m, e := s.Message(ctx, id, true)
	if e != nil {
		return m, false, e
	}
	n, e := s.q(ctx).SupportClaim(ctx, sqlc.SupportClaimParams{ID: id, UpdatedAt: now})
	if n == 1 {
		m.Status = domain.Sending
	}
	return m, n == 1, e
}
func (s *Store) Complete(ctx context.Context, m domain.Message, status, external, reason string, now time.Time) error {
	q := s.q(ctx)
	if _, e := q.SupportLock(ctx, m.ConversationID); e != nil {
		return e
	}
	n, e := q.SupportFinish(ctx, sqlc.SupportFinishParams{ID: m.ID, Status: status, ExternalID: external, Reason: reason, UpdatedAt: now})
	if e != nil {
		return e
	}
	if n != 1 {
		return apperr.Conflict("support send intent no longer active")
	}
	if status == domain.Sent {
		return q.SupportAnswered(ctx, sqlc.SupportAnsweredParams{ID: m.ConversationID, AnsweredThrough: m.ReplyToSequence, UpdatedAt: now})
	}
	return nil
}
func (s *Store) Retry(ctx context.Context, id uuid.UUID, now time.Time) error {
	n, e := s.q(ctx).SupportRetry(ctx, sqlc.SupportRetryParams{ID: id, UpdatedAt: now})
	if e == nil && n != 1 {
		return apperr.Conflict("only failed messages can be retried")
	}
	return e
}
func (s *Store) Waiting(ctx context.Context) (int, error) {
	n, e := s.q(ctx).SupportWaiting(ctx)
	return int(n), e
}

func (s *Store) Recover(ctx context.Context, now time.Time) error {
	ids, e := s.q(ctx).SupportStaleSending(ctx, sqlc.SupportStaleSendingParams{UpdatedAt: now, UpdatedAt_2: now.Add(-15 * time.Minute)})
	if e == nil && len(ids) > 0 {
		s.Log.WarnContext(ctx, "support interrupted sends marked unknown", "count", len(ids))
	}
	return e
}

var _ domain.Store = (*Store)(nil)
