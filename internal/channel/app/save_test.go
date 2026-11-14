package app_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

var errSaveInjected = errors.New("injected save failure")

type saveAudit struct {
	audit.Recorder
	fail bool
}

func (a saveAudit) Record(ctx context.Context, e audit.Entry) error {
	if err := a.Recorder.Record(ctx, e); err != nil {
		return err
	}
	if a.fail && e.Action == "channel.updated" {
		return errSaveInjected
	}
	return nil
}

type savePrices struct{ domain.Prices }

func (p savePrices) Replace(ctx context.Context, id uuid.UUID, prices []domain.Price, now time.Time) error {
	if err := p.Prices.Replace(ctx, id, prices, now); err != nil {
		return err
	}
	return errSaveInjected
}

type saveChannels struct{ domain.Channels }

func (p saveChannels) Save(ctx context.Context, c *domain.Channel) error {
	if err := p.Channels.Save(ctx, c); err != nil {
		return err
	}
	return errSaveInjected
}

// The production notifier enqueues in ctx's transaction. This fake exercises
// that contract without sending any external messages.
type saveNotifier struct {
	queue job.Enqueuer
	fail  bool
}

func (n saveNotifier) Notify(ctx context.Context, notice domain.Notice) error {
	if err := n.queue.Enqueue(ctx, job.Job{Kind: "test_channel_notice", DedupeKey: "test_channel_notice:" + notice.ChannelID.String(), Payload: map[string]any{}, RunAt: start, MaxAttempts: 1}); err != nil {
		return err
	}
	if n.fail {
		return errSaveInjected
	}
	return nil
}

type saveFailJobs struct{}

func (saveFailJobs) Enqueue(context.Context, ...job.Job) error { return errSaveInjected }

type observedCategories struct {
	domain.Categories
	entered chan struct{}
}

func (r observedCategories) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Category, error) {
	close(r.entered)
	return r.Categories.GetForUpdate(ctx, id)
}

func TestChannelSaveSerializesWithCategoryArchive(t *testing.T) {
	e := newEnv(t)
	c := e.moderated(-80000000803)
	category := domain.Category{ID: ids.New(), Slug: "save-" + ids.New().String(), Name: "Temporary", Position: 9000}
	if err := e.store.Categories().Insert(t.Context(), category, start); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = e.d.Owner.Exec(context.Background(), `DELETE FROM channel_category WHERE id=$1`, category.ID)
	}()
	// An admin archives while holding the same row lock used by cabinet save.
	adminTx, err := e.d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer adminTx.Rollback(t.Context())
	if _, err := adminTx.Exec(t.Context(), `UPDATE channel_category SET is_active=false WHERE id=$1`, category.ID); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	e.svc.Categories = observedCategories{Categories: e.svc.Categories, entered: entered}
	prices := []domain.Price{{FormatID: "f_2_48", Amount: 400000, IsEnabled: true}}
	before := e.channel(c.MaxChatID)
	ctx, cancel := context.WithTimeout(e.ctx(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Update(ctx, *c.OrganizationID, c.ID, domain.ProfileUpdate{CategoryID: &category.ID, Prices: &prices})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := adminTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("archived selection accepted: %v", err)
	}
	if !reflect.DeepEqual(before, e.channel(c.MaxChatID)) {
		t.Fatal("prices changed despite concurrent archive")
	}
}

func TestChannelSaveAtomicRollback(t *testing.T) {
	for _, failure := range []string{"price_write", "channel_write", "audit", "job", "notification"} {
		t.Run(failure, func(t *testing.T) {
			e := newEnv(t)
			c := e.active(-80000000801)
			before := e.channel(c.MaxChatID)
			// Changing the confirmed RKN number suspends an active channel and
			// schedules one notification, in the same transaction as prices.
			_, err := e.d.Pool.Exec(t.Context(), `UPDATE channel SET subscribers_count=10000, rkn_status='confirmed' WHERE id=$1`, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			before = e.channel(c.MaxChatID)
			e.svc.Notifier = saveNotifier{queue: e.svc.Jobs, fail: failure == "notification"}
			switch failure {
			case "price_write":
				e.svc.Prices = savePrices{e.svc.Prices}
			case "channel_write":
				e.svc.Channels = saveChannels{e.svc.Channels}
			case "audit":
				e.svc.Audit = saveAudit{Recorder: e.svc.Audit, fail: true}
			case "job":
				e.svc.Notifier = saveNotifier{queue: saveFailJobs{}}
			}
			audits := e.count(`SELECT count(*) FROM audit_log`)
			events := e.count(`SELECT count(*) FROM realtime_event`)
			logsBefore := e.logs.Len()
			description, number := "private description must not be logged", "new-rkn"
			prices := []domain.Price{{FormatID: "f_2_48", Amount: 400000, IsEnabled: true}}
			_, err = e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, domain.ProfileUpdate{Description: &description, RknNumber: &number, Prices: &prices})
			if !errors.Is(err, errSaveInjected) {
				t.Fatalf("failure not returned: %v", err)
			}
			after := e.channel(c.MaxChatID)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("partial channel save after %s", failure)
			}
			if e.count(`SELECT count(*) FROM audit_log`) != audits || e.count(`SELECT count(*) FROM realtime_event`) != events || e.count(`SELECT count(*) FROM job WHERE kind='test_channel_notice'`) != 0 {
				t.Fatal("effects survived rollback")
			}
			for _, line := range strings.Split(e.logs.String()[logsBefore:], "\n") {
				// Repository DEBUG describes a SQL write inside the transaction;
				// application success must only describe a committed save.
				if strings.Contains(line, description) || strings.Contains(line, `"op":"Channel.SaveBlogger"`) &&
					(strings.Contains(line, "channel updated") || strings.Contains(line, "channel prices updated") || strings.Contains(line, "channel status changed")) {
					t.Fatal("uncommitted success or private value logged")
				}
			}
		})
	}
}

func TestChannelSaveValidationAndIdempotentEffects(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000802)
	description := "changed"
	badPrices := []domain.Price{{FormatID: "f_1_24", Amount: 1, IsEnabled: true}}
	before := e.channel(c.MaxChatID)
	_, err := e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, domain.ProfileUpdate{Description: &description, Prices: &badPrices})
	if !apperr.Is(err, apperr.CodeValidationFailed) || !reflect.DeepEqual(before, e.channel(c.MaxChatID)) {
		t.Fatalf("invalid price: %v", err)
	}
	category := ids.New()
	prices := []domain.Price{{FormatID: "f_2_48", Amount: 400000, IsEnabled: true}}
	_, err = e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, domain.ProfileUpdate{CategoryID: &category, Prices: &prices})
	if !apperr.Is(err, apperr.CodeValidationFailed) || !reflect.DeepEqual(before, e.channel(c.MaxChatID)) {
		t.Fatalf("invalid category: %v", err)
	}
	_, err = e.svc.Update(e.ctx(), e.org(), c.ID, domain.ProfileUpdate{CategoryID: &category, Prices: &prices})
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	if _, err := e.d.Pool.Exec(t.Context(), `UPDATE channel SET subscribers_count=10000, rkn_status='confirmed' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.Notifier = saveNotifier{queue: e.svc.Jobs}
	number := "new-number"
	p := domain.ProfileUpdate{Description: &description, RknNumber: &number, Prices: &prices}
	got, err := e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, p)
	if err != nil || got.Status != domain.StatusSuspended || got.Description != description || len(got.Prices) != 1 || got.Prices[0].FormatID != "f_2_48" {
		t.Fatalf("save: %+v %v", got, err)
	}
	audits := e.count(`SELECT count(*) FROM audit_log`)
	if _, err := e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, p); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM audit_log`) != audits || e.count(`SELECT count(*) FROM job WHERE kind='test_channel_notice'`) != 1 {
		t.Fatal("duplicate effects on repeated save")
	}
}
