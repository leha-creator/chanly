package postgres

import (
	"context"
	"time"
)

// Retire locks eligible conversations/staging in a transaction; files are removed
// later using a durable outbox, without holding a database lock across storage I/O.
func (s *Store) Retire(ctx context.Context, now time.Time, retention time.Duration) error {
	q := s.q(ctx)
	conversations, e := q.SupportRetireCandidates(ctx, now.Add(-retention))
	if e != nil {
		return e
	}
	for _, id := range conversations {
		if e = q.SupportRetireBlobs(ctx, id); e != nil {
			return e
		}
		if e = q.SupportRetireConversation(ctx, id); e != nil {
			return e
		}
	}
	orphans, e := q.SupportOrphans(ctx, now.Add(-24*time.Hour))
	if e != nil {
		return e
	}
	for _, id := range orphans {
		if e = q.SupportRetireOrphanBlobs(ctx, id); e != nil {
			return e
		}
		if e = q.SupportDeleteAttachment(ctx, id); e != nil {
			return e
		}
	}
	if e = q.SupportCleanupInbound(ctx, now.Add(-7*24*time.Hour)); e != nil {
		return e
	}
	if e = q.SupportCleanupRevisions(ctx, now.Add(-7*24*time.Hour)); e != nil {
		return e
	}
	s.Log.InfoContext(ctx, "support history retired", "conversations", len(conversations), "staging_attachments", len(orphans))
	return nil
}
func (s *Store) GC(ctx context.Context) ([]string, error) { return s.q(ctx).SupportGC(ctx) }
func (s *Store) GCDone(ctx context.Context, key string) error {
	return s.q(ctx).SupportGCDone(ctx, key)
}
