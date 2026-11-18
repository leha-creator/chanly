package app

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/support/domain"
	"time"
)

func (s *Service) Reply(ctx context.Context, id, actor uuid.UUID, r domain.Reply) (domain.Message, error) {
	if !s.Enabled {
		return domain.Message{}, apperr.New(apperr.CodeServiceUnavailable, "support is disabled")
	}
	if r.ClientKey == uuid.Nil {
		return domain.Message{}, apperr.Validation("client_key", "required")
	}
	if r.ReplyToSequence < 0 {
		return domain.Message{}, apperr.Validation("reply_to_sequence", "out_of_range")
	}
	if e := domain.ValidateReply(r.Text, len(r.Attachments)); e != nil {
		return domain.Message{}, e
	}
	var result domain.Message
	e := s.Tx.InTx(ctx, func(ctx context.Context) error {
		kinds := make([]string, 0, len(r.Attachments))
		for _, id := range r.Attachments {
			a, e := s.Repo.Attachment(ctx, id)
			if e != nil {
				return safeError(e)
			}
			kinds = append(kinds, a.Type)
		}
		if e := domain.ValidateMediaKinds(kinds); e != nil {
			return e
		}
		m, created, e := s.Repo.QueueReply(ctx, id, actor, r, s.Clock.Now())
		if e != nil {
			return safeError(e)
		}
		result = m
		if !created {
			return nil
		}
		if e = s.Jobs.Enqueue(ctx, job.Job{Kind: SendKind, DedupeKey: "support_send:" + m.ID.String(), Payload: Payload{m.ID}, MaxAttempts: 10}); e != nil {
			return safeError(e)
		}
		return s.record(ctx, "support.reply", m.ID)
	})
	if e == nil {
		s.Log.InfoContext(ctx, "support reply queued", "conversation_id", id, "message_id", result.ID, "actor_id", actor)
	}
	return result, e
}

func (s *Service) Retry(ctx context.Context, id uuid.UUID) (domain.Message, error) {
	if !s.Enabled {
		return domain.Message{}, apperr.New(apperr.CodeServiceUnavailable, "support is disabled")
	}
	e := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, e := s.Repo.Message(ctx, id, true); e != nil {
			return safeError(e)
		}
		if e := s.Repo.Retry(ctx, id, s.Clock.Now()); e != nil {
			return safeError(e)
		}
		// A failed result is visible before the previous worker acknowledges its
		// job. Use a fresh retry job so that its live dedupe key cannot swallow
		// this deliberate operator action. The locked state transition dedupes it.
		if e := s.Jobs.Enqueue(ctx, job.Job{Kind: SendKind, DedupeKey: "support_retry:" + id.String() + ":" + ids.New().String(), Payload: Payload{id}, MaxAttempts: 10}); e != nil {
			return safeError(e)
		}
		return s.record(ctx, "support.retry", id)
	})
	if e != nil {
		return domain.Message{}, e
	}
	return s.Repo.Message(ctx, id, false)
}

func (s *Service) Send(ctx context.Context, id uuid.UUID, attempts int) error {
	m, e := s.Repo.Message(ctx, id, false)
	if e != nil {
		return safeError(e)
	}
	if m.Status == domain.Sent || m.Status == domain.Failed || m.Status == domain.Unknown {
		return nil
	}
	if m.Status == domain.Sending {
		return job.Postpone(errors.New("support send intent pending recovery"), 30*time.Second)
	}
	for _, a := range m.Attachments {
		if a.Status == "pending" || a.Status == "processing" {
			return job.Postpone(errors.New("support media preparation pending"), 10*time.Second)
		}
	}
	claimed := false
	e = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var e error
		m, claimed, e = s.Repo.Claim(ctx, id, s.Clock.Now())
		return safeError(e)
	})
	if e != nil {
		return e
	}
	if !claimed {
		return job.Postpone(errors.New("earlier support reply pending"), 5*time.Second)
	}
	s.Log.InfoContext(ctx, "support send started", "message_id", m.ID, "conversation_id", m.ConversationID)
	media, err := s.outgoingMedia(ctx, m)
	var external string
	if err == nil {
		c, e := s.Repo.Conversation(ctx, m.ConversationID, uuid.Nil, false)
		if e != nil {
			return safeError(e)
		}
		external, err = s.Provider.Send(ctx, c.UserID, m.Text, media)
	}
	status, reason := domain.Sent, ""
	retry := false
	if err != nil {
		var d *domain.DeliveryError
		if errors.As(err, &d) && d.Definitive {
			status = domain.Failed
			reason = d.Reason
			if d.Temporary && attempts < 9 {
				status = domain.Queued
				retry = true
			}
		} else {
			status = domain.Unknown
			reason = "outcome_unknown"
		}
	}
	e = s.Tx.InTx(ctx, func(ctx context.Context) error {
		return safeError(s.Repo.Complete(ctx, m, status, external, reason, s.Clock.Now()))
	})
	if e != nil {
		return e
	}
	if status == domain.Unknown {
		s.Log.WarnContext(ctx, "support send result unknown", "message_id", id)
	} else {
		s.Log.InfoContext(ctx, "support send finished", "message_id", id, "status", status, "reason", reason)
	}
	if retry {
		return job.Retry(errors.New("support temporary refusal"), time.Duration(1<<min(attempts, 6))*5*time.Second)
	}
	return nil
}

func (s *Service) outgoingMedia(ctx context.Context, m domain.Message) ([]domain.Attachment, error) {
	out := make([]domain.Attachment, 0, len(m.Attachments))
	for _, public := range m.Attachments {
		a, e := s.Repo.Attachment(ctx, public.ID)
		if e != nil {
			return nil, &domain.DeliveryError{Reason: "attachment_unavailable", Definitive: true}
		}
		if a.Status != "ready" {
			return nil, &domain.DeliveryError{Reason: "attachment_failed", Definitive: true}
		}
		if a.ProviderToken == "" {
			r, _, e := s.Storage.Get(ctx, a.StorageKey)
			if e != nil {
				return nil, &domain.DeliveryError{Reason: "storage_unavailable", Definitive: true, Temporary: true}
			}
			token, e := s.Provider.Upload(ctx, a.Type, a.Name, r)
			r.Close()
			if e != nil {
				var refusal *domain.DeliveryError
				if errors.As(e, &refusal) {
					return nil, refusal
				}
				return nil, &domain.DeliveryError{Reason: "upload_unavailable", Definitive: true, Temporary: true}
			}
			a.ProviderToken = token
			if e = s.Repo.SaveAttachment(ctx, a); e != nil {
				return nil, &domain.DeliveryError{Reason: "upload_checkpoint_failed", Definitive: true, Temporary: true}
			}
		}
		out = append(out, a)
	}
	return out, nil
}
