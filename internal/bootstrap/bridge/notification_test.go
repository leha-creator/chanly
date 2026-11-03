package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	notificationdomain "github.com/leha-creator/chanly/internal/notification/domain"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type recordingNotifier struct{ got []notificationdomain.Notice }

func (r *recordingNotifier) Notify(_ context.Context, n notificationdomain.Notice) error {
	r.got = append(r.got, n)
	return nil
}

func TestPlacementNotifierMapsRole(t *testing.T) {
	rec := &recordingNotifier{}
	b := PlacementNotifier{Svc: rec}
	org := uuid.New()
	for _, actor := range []placementdomain.ActorType{placementdomain.ActorAdvertiser, placementdomain.ActorBlogger, placementdomain.ActorAdmin} {
		if err := b.Notify(context.Background(), placementdomain.Notice{Template: "placement.accepted", OrganizationID: org,
			Role: actor, ObjectType: "placement", ObjectID: "p1", Key: "k", Data: map[string]any{"a": 1}}); err != nil {
			t.Fatal(err)
		}
	}
	want := []notificationdomain.Role{notificationdomain.RoleAdvertiser, notificationdomain.RoleBlogger, notificationdomain.RoleAdmin}
	for i, n := range rec.got {
		if n.Role != want[i] || n.Key != "k" || n.OrganizationID != org || n.ObjectID != "p1" || n.Data["a"] != 1 {
			t.Fatalf("notice %d: %+v", i, n)
		}
	}
}

func TestBillingNotifierAdvertiser(t *testing.T) {
	rec := &recordingNotifier{}
	b := BillingNotifier{Svc: rec}
	if err := b.Notify(context.Background(), billingdomain.Notice{Template: "balance.deposited"}); err == nil {
		t.Fatal("notice without organization must fail")
	}
	org := uuid.New()
	if err := b.Notify(context.Background(), billingdomain.Notice{Template: "balance.deposited", OrganizationID: org}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 || rec.got[0].Role != notificationdomain.RoleAdvertiser || rec.got[0].OrganizationID != org {
		t.Fatalf("got %+v", rec.got)
	}
}

func TestNotificationMessengerPassesRejection(t *testing.T) {
	fake := maxfake.New()
	b := NotificationMessenger{MAX: fake, Log: logx.Discard()}
	if err := b.SendUserMessage(context.Background(), 42, "hi"); err != nil {
		t.Fatal(err)
	}
	fake.FailUserMessagesWith(job.Permanent(errors.New("chat.denied")))
	if err := b.SendUserMessage(context.Background(), 42, "hi"); !job.IsPermanent(err) {
		t.Fatalf("rejection must stay permanent: %v", err)
	}
}

func TestPayoutNotifierBlogger(t *testing.T) {
	rec := &recordingNotifier{}
	b := PayoutNotifier{Svc: rec}
	if err := b.Notify(context.Background(), payoutdomain.Notice{Template: payoutdomain.NoticePayoutSent}); err == nil {
		t.Fatal("notice without organization must fail")
	}
	org, payout, contractor := uuid.New(), uuid.New(), uuid.New()
	if err := b.Notify(context.Background(), payoutdomain.Notice{Template: payoutdomain.NoticePayoutSucceeded, OrganizationID: org,
		ObjectID: payout, Data: map[string]any{"receipt_url": "https://r"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Notify(context.Background(), payoutdomain.Notice{Template: payoutdomain.NoticeRocketworkActive, OrganizationID: org,
		ObjectID: contractor}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 2 {
		t.Fatalf("got %+v", rec.got)
	}
	p, c := rec.got[0], rec.got[1]
	if p.Role != notificationdomain.RoleBlogger || p.ObjectType != "payout" || p.ObjectID != payout.String() ||
		p.Key != "payout.succeeded:"+payout.String() || p.Data["receipt_url"] != "https://r" {
		t.Fatalf("payout notice %+v", p)
	}
	if c.Role != notificationdomain.RoleBlogger || c.ObjectType != "rocketwork_contractor" || c.Key != "" {
		t.Fatalf("contractor notice %+v", c)
	}
	if notificationdomain.Link(p) != "/blg/payouts" || notificationdomain.Link(c) != "/blg/payouts" {
		t.Fatalf("links %q %q", notificationdomain.Link(p), notificationdomain.Link(c))
	}
}
