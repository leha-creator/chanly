package app

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/support/domain"
	"log/slog"
	"net/http"
	"time"
)

const SendKind = "send_support_message"
const MediaKind = "prepare_support_media"

type Repository interface {
	domain.Store
	Ingest(context.Context, domain.Incoming, time.Time) (domain.Message, bool, error)
	Revise(context.Context, domain.Incoming) error
	Recover(context.Context, time.Time) error
	AttachmentIDs(context.Context, uuid.UUID) ([]uuid.UUID, error)
	ClaimMedia(context.Context, uuid.UUID, time.Time) (bool, error)
	Retire(context.Context, time.Time, time.Duration) error
	GC(context.Context) ([]string, error)
	GCDone(context.Context, string) error
}
type Auditor interface {
	Record(context.Context, audit.Entry) error
}
type Service struct {
	Retention  time.Duration
	Media      MediaProcessor
	Fetch      *http.Client
	MediaHosts []string
	Repo       Repository
	Tx         tx.Runner
	Jobs       job.Enqueuer
	Clock      clock.Clock
	Log        *slog.Logger
	Provider   domain.Provider
	Storage    storage.ObjectStorage
	Audit      Auditor
	Enabled    bool
	Limits     domain.Limits
}
type Payload struct {
	ID uuid.UUID `json:"id"`
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	return errors.New("support operation failed")
}

func (s *Service) Ingest(ctx context.Context, in domain.Incoming) error {
	s.Log.DebugContext(ctx, "support incoming started", "event_type", in.Type)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if in.Type != "message_created" {
			return s.Repo.Revise(ctx, in)
		}
		m, created, e := s.Repo.Ingest(ctx, in, s.Clock.Now())
		if e != nil {
			return safeError(e)
		}
		if !created {
			return nil
		}
		for _, a := range m.Attachments {
			if a.Status == "pending" {
				if e = s.Jobs.Enqueue(ctx, job.Job{Kind: MediaKind, DedupeKey: "support_media:" + a.ID.String(), Payload: Payload{a.ID}, MaxAttempts: 5}); e != nil {
					return e
				}
			}
		}
		s.Log.InfoContext(ctx, "support incoming saved", "conversation_id", m.ConversationID, "message_id", m.ID)
		return nil
	})
	return err
}
