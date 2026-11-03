package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

// listener — поддельный модуль размещений: запоминает смены и может отказать.
type listener struct {
	changes []domain.VersionChange
	inTx    []bool
	failOn  domain.VersionStatus
}

func (l *listener) VersionChanged(ctx context.Context, c domain.VersionChange) error {
	_, open := tx.FromContext(ctx)
	l.changes = append(l.changes, c)
	l.inTx = append(l.inTx, open)
	if c.To == l.failOn {
		return errors.New("placements refused")
	}
	return nil
}

func TestVersionListenerInTransaction(t *testing.T) {
	e := newEnv(t)
	l := &listener{failOn: domain.VersionApproved}
	e.svc.SetVersionListener(l)
	c := e.readyDraft(domain.CarrierText)
	v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1001)
	if err != nil {
		t.Fatal(err)
	}
	// Ошибка слушателя откатывает переход версии.
	if _, err := e.svc.ApproveVersion(e.ctx(), v.ID, e.user); err == nil {
		t.Fatal("listener error ignored")
	}
	got, err := e.store.Versions().Get(context.Background(), v.ID)
	if err != nil || got.Status != domain.VersionPendingModeration {
		t.Fatalf("version after rollback: %s %v", got.Status, err)
	}
	l.failOn = ""
	if _, err := e.svc.ApproveVersion(e.ctx(), v.ID, e.user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MarkVersionReady(e.ctx(), v.ID, "E1"); err != nil {
		t.Fatal(err)
	}
	want := []domain.VersionChange{
		{VersionID: v.ID, PostID: v.PostID, From: domain.VersionPendingModeration, To: domain.VersionApproved},
		{VersionID: v.ID, PostID: v.PostID, From: domain.VersionPendingModeration, To: domain.VersionApproved},
		{VersionID: v.ID, PostID: v.PostID, From: domain.VersionApproved, To: domain.VersionReady},
	}
	if len(l.changes) != len(want) {
		t.Fatalf("changes %+v", l.changes)
	}
	for i, w := range want {
		if l.changes[i] != w || !l.inTx[i] {
			t.Errorf("change %d: %+v in tx %t", i, l.changes[i], l.inTx[i])
		}
	}
}

// insertOrder записывает заказ кампании напрямую: таблица принадлежит модулю размещений.
func (e *env) insertOrder(campaignID uuid.UUID, versionID uuid.UUID) uuid.UUID {
	e.t.Helper()
	id := ids.New()
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO "order" (id, number, organization_id, campaign_id, post_id,
		current_post_version_id, status, total_amount, created_by, created_at, updated_at)
		SELECT $1, nextval('order_number_seq'), organization_id, campaign_id, id, $3, 'moderation', 0, $4, now(), now()
		FROM post WHERE campaign_id = $2`, id, campaignID, versionID, e.user); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestCampaignOrderedAndArchived(t *testing.T) {
	e := newEnv(t)
	c := e.readyDraft(domain.CarrierText)
	locked, err := e.svc.LockCampaignForCheckout(e.ctx(), e.org, c.ID)
	if err != nil || locked.Campaign.ID != c.ID || locked.PostID == uuid.Nil {
		t.Fatalf("lock: %+v %v", locked, err)
	}
	other, _ := posttest.Advertiser(t, e.d.Pool)
	if _, err := e.svc.LockCampaignForCheckout(e.ctx(), other, c.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign campaign: %v", err)
	}
	v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 1001)
	if err != nil {
		t.Fatal(err)
	}
	orderID := e.insertOrder(c.ID, v.ID)
	if err := e.svc.MarkCampaignOrdered(e.ctx(), e.org, c.ID, orderID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.GetCampaign(e.ctx(), e.org, c.ID)
	if got.Status != domain.CampaignOrdered || got.OrderID == nil || *got.OrderID != orderID {
		t.Fatalf("ordered campaign %+v", got)
	}
	wantReason(t, e.svc.MarkCampaignOrdered(e.ctx(), e.org, c.ID, orderID), apperr.CodeConflict, "not_draft")

	// S-29: черновик оплаченной кампании правится.
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Новый текст"))); err != nil {
		t.Fatalf("draft of ordered campaign: %v", err)
	}
	if err := e.svc.ArchiveCampaign(context.Background(), e.org, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ArchiveCampaign(context.Background(), e.org, c.ID); err != nil {
		t.Fatalf("repeated archive: %v", err)
	}
	if _, err := e.svc.SaveDraft(e.ctx(), e.org, e.user, c.ID, draftInput(doc("Ещё текст"))); err == nil {
		t.Fatal("draft of archived campaign saved")
	} else {
		wantReason(t, err, apperr.CodeConflict, "campaign_archived")
	}
	name := "Новое имя"
	if got, err := e.svc.UpdateCampaign(e.ctx(), e.org, e.user, c.ID, domain.CampaignInput{Name: &name}); err != nil || got.Name != name ||
		got.Status != domain.CampaignArchived {
		t.Fatalf("rename archived: %+v %v", got, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action IN ('campaign.ordered', 'campaign.archived')`); n != 2 {
		t.Fatalf("audit entries %d", n)
	}
	if !strings.Contains(e.logs.String(), `"msg":"campaign status changed"`) {
		t.Fatal("campaign status change not logged")
	}
}

// markOrdered переводит кампанию в ordered напрямую: версия и заказ записываются в базу
// без сценариев (кампания без готового черновика).
func (e *env) markOrdered(campaignID uuid.UUID) {
	e.t.Helper()
	version := ids.New()
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO post_version (id, post_id, number, status, content_doc, body,
		token_carrier, token_position, advertiser_name, advertiser_inn, created_by, created_at)
		SELECT $1, id, 1, 'pending_moderation', '{}', 'текст', 'text', 'below', 'ООО', '7707083893', $3, now()
		FROM post WHERE campaign_id = $2`, version, campaignID, e.user); err != nil {
		e.t.Fatal(err)
	}
	order := e.insertOrder(campaignID, version)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE campaign SET status = 'ordered', order_id = $2 WHERE id = $1`,
		campaignID, order); err != nil {
		e.t.Fatal(err)
	}
}
