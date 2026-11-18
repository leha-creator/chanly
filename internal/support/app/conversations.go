package app

import (
	"context"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/support/domain"
)

func (s *Service) List(ctx context.Context, f domain.Filter) ([]domain.Conversation, int, error) {
	s.Log.DebugContext(ctx, "support list", "actor_id", f.ActorID, "offset", f.Offset, "limit", f.Limit)
	if !s.Enabled {
		return []domain.Conversation{}, 0, nil
	}
	items, n, e := s.Repo.List(ctx, f)
	return items, n, safeError(e)
}
func (s *Service) Conversation(ctx context.Context, id, actor uuid.UUID) (domain.Conversation, error) {
	c, e := s.Repo.Conversation(ctx, id, actor, false)
	return c, safeError(e)
}
func (s *Service) Messages(ctx context.Context, id, actor uuid.UUID, before int64, limit int32) ([]domain.Message, error) {
	if _, e := s.Conversation(ctx, id, actor); e != nil {
		return nil, e
	}
	m, e := s.Repo.Messages(ctx, id, before, limit)
	return m, safeError(e)
}
func (s *Service) Read(ctx context.Context, id, actor uuid.UUID, seq int64) error {
	if seq < 0 {
		return apperr.Validation("sequence", "out_of_range")
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, e := s.Repo.Conversation(ctx, id, actor, true); e != nil {
			return safeError(e)
		}
		if e := s.Repo.MarkRead(ctx, id, actor, seq); e != nil {
			return safeError(e)
		}
		s.Log.DebugContext(ctx, "support read advanced", "conversation_id", id, "actor_id", actor, "sequence", seq)
		return nil
	})
}
func (s *Service) Update(ctx context.Context, id, actor uuid.UUID, version int64, status string, assignee *uuid.UUID) (domain.Conversation, error) {
	if status != "open" && status != "closed" {
		return domain.Conversation{}, apperr.Validation("status", "invalid")
	}
	e := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, e := s.Repo.Conversation(ctx, id, actor, true); e != nil {
			return safeError(e)
		}
		if e := s.Repo.Update(ctx, id, version, status, assignee, s.Clock.Now()); e != nil {
			return safeError(e)
		}
		return s.record(ctx, "support.update", id)
	})
	if e != nil {
		return domain.Conversation{}, e
	}
	s.Log.InfoContext(ctx, "support conversation updated", "conversation_id", id, "actor_id", actor, "status", status)
	return s.Conversation(ctx, id, actor)
}
func (s *Service) record(ctx context.Context, action string, id uuid.UUID) error {
	if s.Audit == nil {
		return nil
	}
	return s.Audit.Record(ctx, audit.Entry{Action: action, ObjectType: "support", ObjectID: id.String(), ActorType: audit.ActorAdmin})
}
