package app_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/domain"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

func placementStatus(o placementapp.OrderView) placementdomain.Status {
	return o.Placements[0].Placement.Status
}

func TestApprovedVersionGetsErid(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.approve(o.Version.ID)

	c := e.creative(o.Version.ID)
	if c.Sync.Status != domain.SyncPending || c.HasErid() {
		t.Fatalf("creative after approval: %+v", c)
	}
	if o = e.getOrder(o.Order.ID); o.Order.Status != placementdomain.OrderMarking {
		t.Fatalf("order status %s, want marking", o.Order.Status)
	}
	e.settle(6)

	c = e.creative(o.Version.ID)
	if !c.HasErid() || c.Erid != "fake-erid-1" || c.ContractID == uuid.Nil {
		t.Fatalf("creative: %+v", c)
	}
	var body ord.Creative
	obj, _ := e.ord.Object(c.ID.String())
	if err := obj.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.KKTUCodes[0] != kktu || body.Form != domain.FormTextBlock || len(body.URLs) != 1 ||
		!strings.Contains(body.URLs[0], "utm_campaign=order_") || body.TextData[0] != "Скидки в магазине" ||
		body.ContractIDs[0] != c.ContractID.String() || body.Description != "Скидки в магазине" {
		t.Fatalf("creative request: %+v", body)
	}
	v, err := e.post.GetVersion(context.Background(), o.Version.ID)
	if err != nil || v.Version.Status != postdomain.VersionReady || v.Version.Erid != c.Erid ||
		!strings.Contains(v.Version.FinalText, c.Erid) {
		t.Fatalf("version: %+v %v", v.Version, err)
	}
	o = e.getOrder(o.Order.ID)
	if o.Order.Status != placementdomain.OrderInProgress || placementStatus(o) != placementdomain.StatusAwaitingBlogger {
		t.Fatalf("order %s, placement %s", o.Order.Status, placementStatus(o))
	}
	if len(e.logged("erid received")) != 1 {
		t.Fatal("erid received not logged once")
	}

	// После erid креатив не отправляется повторно (D-03).
	calls := e.ord.Calls(ord.OpCreative)
	if err := e.svc.Sync(context.Background(), c.Ref(), 1); err != nil {
		t.Fatal(err)
	}
	e.settle(4)
	if e.ord.Calls(ord.OpCreative) != calls {
		t.Fatal("creative with erid was submitted again")
	}
	if c = e.creative(o.Version.ID); c.Sync.Status != domain.SyncSynced || c.ErirStatus != domain.ErirSuccess {
		t.Fatalf("creative after ERIR: %+v", c)
	}
}

func TestEridOnlyInStatus(t *testing.T) {
	e := newEnv(t)
	e.ord.TokenOnlyInStatus()
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.approve(o.Version.ID)
	e.settle(3)
	for range 5 {
		if e.creative(o.Version.ID).HasErid() {
			break
		}
		e.advance(time.Minute)
	}
	c := e.creative(o.Version.ID)
	if !c.HasErid() {
		t.Fatalf("erid not taken from status: %+v", c)
	}
	if v, _ := e.post.GetVersion(context.Background(), o.Version.ID); v.Version.Status != postdomain.VersionReady {
		t.Fatalf("version %s after erid from status", v.Version.Status)
	}
}

func TestCreativeWaitsForPublicImages(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	// Изображение готово, но копирование в публичную область не проходит (R-9).
	img, err := e.post.Upload(e.ctx(), e.adv, e.advUser, "photo.jpg", imagingtest.JPEG(800, 600, 0))
	if err != nil {
		t.Fatal(err)
	}
	e.run()
	e.files.FailWith(storagefake.OpCopyToPublic, errors.New("storage unavailable"))
	o := e.orderWithMedia(ch, img.File.ID)
	e.approve(o.Version.ID)
	e.settle(6)
	if c := e.creative(o.Version.ID); c.Sync.Status != domain.SyncPending || e.ord.Calls(ord.OpCreative) != 0 {
		t.Fatalf("creative sent before images are public: %+v", c.Sync)
	}
	if len(e.logged("creative waits for media")) == 0 {
		t.Fatal("media wait not logged")
	}
	e.files.FailWith(storagefake.OpCopyToPublic, nil)
	for range 4 {
		e.advance(time.Hour)
	}
	c := e.creative(o.Version.ID)
	var body ord.Creative
	obj, _ := e.ord.Object(c.ID.String())
	if !c.HasErid() || obj.Decode(&body) != nil || body.Form != domain.FormTextGraphicBlock || len(body.MediaData) != 1 ||
		!strings.HasPrefix(body.MediaData[0].MediaURL, storagefake.PublicBaseURL) {
		t.Fatalf("creative with image: %+v %+v", c, body)
	}
}

func TestVersionReadyBypassingMarking(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.approve(o.Version.ID)
	if _, err := e.post.MarkVersionReady(context.Background(), o.Version.ID, "E-manual"); err != nil {
		t.Fatal(err)
	}
	e.settle(4)
	if _, err := e.store.Creatives().ByVersion(context.Background(), o.Version.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("creative of version ready without marking: %v", err)
	}
	if e.ord.Calls(ord.OpCreative) != 0 {
		t.Fatal("creative sent for a ready version")
	}
}

func TestContractErrorKeepsOrderInMarking(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.ord.FailNext(ord.OpContract, http.StatusUnprocessableEntity, "body.date: field required")
	e.approve(o.Version.ID)
	e.settle(6)
	o = e.getOrder(o.Order.ID)
	if o.Order.Status != placementdomain.OrderMarking || placementStatus(o) != placementdomain.StatusCreated {
		t.Fatalf("order %s, placement %s", o.Order.Status, placementStatus(o))
	}
	if c := e.creative(o.Version.ID); c.Sync.Status != domain.SyncPending || e.ord.Calls(ord.OpCreative) != 0 {
		t.Fatalf("creative with failed contract: %+v", c.Sync)
	}
	if len(e.logged("ord dependency failed")) == 0 {
		t.Fatal("failed dependency not logged")
	}
}

// orderWithMedia — заказ с постом из текста и изображения.
func (e *env) orderWithMedia(channel, file uuid.UUID) placementapp.OrderView {
	e.t.Helper()
	if _, err := e.placements.AddCartItem(e.ctx(), e.adv, e.advUser, channel, "f_1_24"); err != nil {
		e.t.Fatal(err)
	}
	name := "Кампания с фото"
	from, to := postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22)
	tf, tt := postdomain.TimeOfDay(9*60), postdomain.TimeOfDay(21*60)
	c, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{
		Name: &name, DateFrom: &from, DateTo: &to, TimeFrom: &tf, TimeTo: &tt,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	carrier := postdomain.CarrierText
	doc := postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{{Text: "Фото"}}}}}
	if _, err := e.post.SaveDraft(e.ctx(), e.adv, e.advUser, c.ID, postapp.DraftInput{Doc: doc, MediaIDs: []uuid.UUID{file}, TokenCarrier: &carrier}); err != nil {
		e.t.Fatal(err)
	}
	e.run() // производная для канала
	o, err := e.placements.Checkout(e.ctx(), e.adv, e.advUser, c.ID, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return e.getOrder(o.ID)
}

func TestErirRejectionAfterErid(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.approve(o.Version.ID)
	// Keep the creative pending until rejection, regardless of job ordering.
	// Once async success is reached, polling correctly stops.
	e.ord.HoldStatus(e.creative(o.Version.ID).ID.String(), true)
	e.settle(5)
	c := e.creative(o.Version.ID)
	if !c.HasErid() {
		t.Fatalf("no erid: %+v", c)
	}
	if o = e.getOrder(o.Order.ID); placementStatus(o) != placementdomain.StatusAwaitingBlogger {
		t.Fatalf("placement %s before rejection", placementStatus(o))
	}
	e.ord.RejectObject(c.ID.String(), domain.OrdErirAsyncError, "креатив не соответствует требованиям")
	e.settle(3)
	c = e.creative(o.Version.ID)
	if c.Sync.Status != domain.SyncError || c.ErirStatus != domain.ErirError || c.Erid == "" {
		t.Fatalf("creative after ERIR rejection: %+v", c)
	}
	o = e.getOrder(o.Order.ID)
	p := o.Placements[0].Placement
	if p.Status != placementdomain.StatusCreated || p.StatusReason != placementdomain.ReasonErirRejected || p.PostVersionID != o.Version.ID {
		t.Fatalf("placement after rejection: %s %s", p.Status, p.StatusReason)
	}
	if !e.alerts.has("ord_creative_rejected") {
		t.Fatal("no ord_creative_rejected alert")
	}
}
