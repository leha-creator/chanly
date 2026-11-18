package app

import (
	"context"
	"time"
)

func (s *Service) Cleanup(ctx context.Context) error {
	retention := s.Retention
	if retention <= 0 {
		retention = 180 * 24 * time.Hour
	}
	if e := s.Tx.InTx(ctx, func(ctx context.Context) error { return safeError(s.Repo.Retire(ctx, s.Clock.Now(), retention)) }); e != nil {
		return e
	}
	keys, e := s.Repo.GC(ctx)
	if e != nil {
		return safeError(e)
	}
	for _, key := range keys {
		if key != "" {
			if e = s.Storage.Delete(ctx, key); e != nil {
				s.Log.WarnContext(ctx, "support private object cleanup deferred")
				continue
			}
		}
		if e = s.Repo.GCDone(ctx, key); e != nil {
			return safeError(e)
		}
	}
	return nil
}
