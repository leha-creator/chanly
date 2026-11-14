package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/post/app"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestAlbumOldPendingTemplateIsSkipped(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	d, _, err := e.store.Derivatives().Ensure(e.ctx(), files[0], domain.KindLabeled, domain.ParamsHash(domain.KindLabeled, 1, posttest.Name, "7707083893"), e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	objects := e.files.Len()
	if err = e.svc.BuildDerivative(e.ctx(), app.BuildPayload{DerivativeID: d.ID, OrganizationID: e.org}); err != nil {
		t.Fatal(err)
	}
	d, err = e.store.Derivatives().Get(e.ctx(), d.ID)
	if err != nil || d.Ready() || e.files.Len() != objects {
		t.Fatal("v2 rendered into a v1 record")
	}
	c := e.campaign("Восстановление")
	saveAlbum(e, c.ID, files)
	e.drain()
	assertAlbum(e, c.ID, files)
}

func TestAlbumMutationsWaitForOrganizationLock(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	c := e.campaign("Конкурентный альбом")
	saveAlbum(e, c.ID, files)
	e.drain()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Holding the documented advisory lock must block all three entry points.
	for _, op := range []string{"save", "version", "delete"} {
		t.Run(op, func(t *testing.T) {
			locker, err := e.d.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Rollback(ctx)
			if _, err = locker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('post-media:' || CAST($1::uuid AS text),0))`, e.org); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				switch op {
				case "save":
					_, err := e.svc.SaveDraft(ctx, e.org, e.user, c.ID, draftInput(doc("Порядок после блокировки"), files...))
					result <- err
				case "version":
					_, err := e.svc.CreateVersion(ctx, e.org, c.ID, e.user, 1001)
					result <- err
				case "delete":
					result <- e.svc.Delete(ctx, e.org, e.user, files[0])
				}
			}()
			// Observe PostgreSQL rather than relying on a sleep to prove the waiter reached the lock.
			for {
				var waiting bool
				if err = e.d.Owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("%s bypassed lock: %v", op, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err = locker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
	// The deletion has committed. No subsequent snapshot may resurrect A.
	e.drain()
	v := assertAlbum(e, c.ID, files[1:])
	for _, m := range v.Media {
		if m.MediaFileID == files[0] {
			t.Fatal("deleted file entered new snapshot")
		}
	}
	_, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Нельзя вернуть удалённый"), files...))
	if !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("deleted media accepted: %v", err)
	}
}

func TestDeleteAlbumQueuesAndDraftRollBackTogether(t *testing.T) {
	e := newEnv(t)
	files := albumFiles(e)
	c := e.campaign("Откат удаления")
	saveAlbum(e, c.ID, files)
	e.drain()
	before, err := e.svc.GetDraft(e.ctx(), e.org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Deps are injected: fail only enqueue, preserving the production SQL transaction.
	original := e.svc.Jobs
	e.svc.Jobs = failedAlbumJobs{}
	err = e.svc.Delete(e.ctx(), e.org, e.user, files[0])
	e.svc.Jobs = original
	if err == nil {
		t.Fatal("delete accepted failed enqueue")
	}
	after, err := e.svc.GetDraft(e.ctx(), e.org, c.ID)
	if err != nil || len(after.Media) != len(before.Media) || after.Media[0].File.ID != files[0] {
		t.Fatalf("draft delete did not roll back: %v", err)
	}
	if _, err = e.store.MediaFiles().Get(e.ctx(), e.org, files[0]); err != nil {
		t.Fatal("file soft delete did not roll back")
	}
}

type failedAlbumJobs struct{}

func (failedAlbumJobs) Enqueue(context.Context, ...job.Job) error {
	return errors.New("test enqueue failure")
}
