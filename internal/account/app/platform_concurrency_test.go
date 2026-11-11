package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

type pausedPlatformInsert struct {
	domain.PlatformOrganizations
	inserted chan struct{}
	resume   <-chan struct{}
	rollback error
}

func (p pausedPlatformInsert) InsertPlatform(ctx context.Context, org domain.PlatformOrganization, now time.Time) error {
	if err := p.PlatformOrganizations.InsertPlatform(ctx, org, now); err != nil {
		return err
	}
	close(p.inserted)
	select {
	case <-p.resume:
		return p.rollback
	case <-ctx.Done():
		return ctx.Err()
	}
}

type observedPlatformRead struct {
	domain.PlatformOrganizations
	pid chan int32
}

func (p observedPlatformRead) Platform(ctx context.Context) (*domain.PlatformOrganization, error) {
	txn, ok := tx.FromContext(ctx)
	if !ok {
		return nil, errors.New("test startup has no transaction")
	}
	var pid int32
	if err := txn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		return nil, err
	}
	p.pid <- pid
	return p.PlatformOrganizations.Platform(ctx)
}

func TestEnsurePlatformOrganizationConcurrentStartup(t *testing.T) {
	for _, scenario := range []string{"commit", "rollback", "conflicting_config"} {
		t.Run(scenario, func(t *testing.T) {
			e := newEnv(t)
			if _, err := e.store.Organizations().Platform(t.Context()); err == nil {
				t.Fatal("platform bootstrap accepted a missing transaction")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			resume := make(chan struct{})
			inserted := make(chan struct{})
			pidChannel := make(chan int32, 1)
			var once sync.Once
			var workers sync.WaitGroup
			unblock := func() { once.Do(func() { close(resume) }) }
			defer func() { unblock(); cancel(); workers.Wait() }()
			rollback := errors.New("first startup rolled back")
			var insertError error
			if scenario == "rollback" {
				insertError = rollback
			}
			first, second := *e.svc, *e.svc
			first.Platform = pausedPlatformInsert{PlatformOrganizations: e.store.Organizations(), inserted: inserted, resume: resume, rollback: insertError}
			second.Platform = observedPlatformRead{PlatformOrganizations: e.store.Organizations(), pid: pidChannel}
			in := app.PlatformOrg{INN: accounttest.INN(domain.SoleProprietor), LegalType: domain.SoleProprietor, Name: "Concurrent platform"}
			other := in
			if scenario == "conflicting_config" {
				other.INN = accounttest.INN(domain.SoleProprietor)
			}
			type result struct {
				id  uuid.UUID
				err error
			}
			firstDone, secondDone := make(chan result, 1), make(chan result, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				id, err := first.EnsurePlatformOrganization(ctx, in)
				firstDone <- result{id, err}
			}()
			select {
			case <-inserted:
			case got := <-firstDone:
				t.Fatalf("first startup failed before barrier: %v", got.err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				id, err := second.EnsurePlatformOrganization(ctx, other)
				secondDone <- result{id, err}
			}()
			var pid int32
			select {
			case pid = <-pidChannel:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Hold the first INSERT uncommitted until the second process actually waits
			// on a PostgreSQL lock. Without the fix it waits on INSERT uniqueness and
			// misreports the winner's platform as an ordinary organization's INN.
			waiting := false
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				if err := e.d.Owner.QueryRow(ctx, `SELECT coalesce(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("second startup never waited on the first transaction")
			}
			unblock()
			var a, b result
			select {
			case a = <-firstDone:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case b = <-secondDone:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if scenario == "rollback" {
				if !errors.Is(a.err, rollback) {
					t.Fatalf("first error=%v", a.err)
				}
			} else if a.err != nil {
				t.Fatal(a.err)
			}
			if scenario == "conflicting_config" {
				if !errors.Is(b.err, app.ErrPlatformConflict) {
					t.Fatalf("different config error=%v", b.err)
				}
			} else {
				if b.err != nil {
					t.Fatal(b.err)
				}
				if b.id == uuid.Nil || (scenario == "commit" && a.id != b.id) {
					t.Fatalf("startup IDs first=%s second=%s", a.id, b.id)
				}
			}
			for _, query := range []string{`SELECT count(*) FROM organization WHERE is_platform`, `SELECT count(*) FROM audit_log WHERE action='organization.platform_created'`} {
				var count int
				if err := e.d.Pool.QueryRow(ctx, query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("singleton/audit count=%d err=%v", count, err)
				}
			}
		})
	}
}
