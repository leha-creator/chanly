package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/markingtest"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

// The production fake intentionally does not retain image contents. Capture at
// its port in this test only, leaving runtime integration behavior untouched.
type albumUploadCapture struct {
	placementdomain.ChannelPublisher
	hashes [][32]byte
}

func (c *albumUploadCapture) UploadImage(ctx context.Context, chatID int64, r io.Reader, size int64, mime string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	c.hashes = append(c.hashes, sha256.Sum256(data))
	return c.ChannelPublisher.UploadImage(ctx, chatID, bytes.NewReader(data), size, mime)
}

func TestFirstImageAlbumPreviewORDAndMAX(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	capture := &albumUploadCapture{ChannelPublisher: e.placements.Publisher}
	e.placements.Publisher = capture
	blogger, user := placementtest.Blogger(t, e.d.Pool)
	markingtest.Accept(t, e.d.Pool, blogger, markingtest.BloggerOffer, start.Add(-24*time.Hour))
	ch := placementtest.Channel(t, e.d.Pool, blogger, "Альбомный канал", 1_000_000)
	if _, err := e.placements.AddCartItem(e.ctx(), e.adv, e.advUser, ch, "f_1_24"); err != nil {
		t.Fatal(err)
	}
	name := "Кампания с альбомом"
	from, to := postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22)
	tf, tt := postdomain.TimeOfDay(9*60), postdomain.TimeOfDay(21*60)
	c, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{Name: &name, DateFrom: &from, DateTo: &to, TimeFrom: &tf, TimeTo: &tt})
	if err != nil {
		t.Fatal(err)
	}
	files := []uuid.UUID{}
	for i := range 3 {
		v, err := e.post.Upload(e.ctx(), e.adv, e.advUser, "photo.png", imagingtest.OpaquePNG(640+i, 480))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, v.File.ID)
	}
	input := postapp.DraftInput{Doc: postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{{Text: "Альбом для проверки маркировки"}}}}}, MediaIDs: files}
	if _, err = e.post.SaveDraft(e.ctx(), e.adv, e.advUser, c.ID, input); err != nil {
		t.Fatal(err)
	}
	e.run()
	preview, err := e.post.Preview(e.ctx(), e.adv, c.ID)
	if err != nil || len(preview.Problems) != 0 || len(preview.MediaURLs) != 3 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	want := [][32]byte{}
	for _, raw := range preview.MediaURLs {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		o, ok := e.files.Object(strings.TrimPrefix(u.Path, "/"))
		if !ok {
			t.Fatal("preview object missing")
		}
		want = append(want, sha256.Sum256(o.Data))
	}
	order, err := e.placements.Checkout(e.ctx(), e.adv, e.advUser, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	o := e.getOrder(order.ID)
	view, err := e.post.GetVersion(ctx, o.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range view.Version.Media {
		var kind string
		if err = e.d.Pool.QueryRow(ctx, `SELECT kind FROM media_derivative WHERE id=$1`, m.DerivativeID).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != string(postdomain.NeededKindAt(postdomain.CarrierMedia, i)) {
			t.Fatalf("position %d: kind %s", i, kind)
		}
		r, err := e.post.OpenDerivative(ctx, m.DerivativeID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || sha256.Sum256(data) != want[i] {
			t.Fatal("preview/snapshot bytes differ")
		}
	}
	// Only the first object is public. ORD must wait for the remaining two.
	if err = e.post.PublishDerivative(ctx, view.Version.Media[0].DerivativeID); err != nil {
		t.Fatal(err)
	}
	e.files.FailWith(storagefake.OpCopyToPublic, errors.New("test public copy unavailable"))
	e.approve(o.Version.ID)
	e.settle(6)
	if e.ord.Calls(ord.OpCreative) != 0 {
		t.Fatal("partial album sent to ORD")
	}
	e.files.FailWith(storagefake.OpCopyToPublic, nil)
	for range 4 {
		e.advance(time.Hour)
	}
	creative := e.creative(o.Version.ID)
	if !creative.HasErid() {
		t.Fatal("no erid after public copies became available")
	}
	obj, _ := e.ord.Object(creative.ID.String())
	var payload ord.Creative
	if err = obj.Decode(&payload); err != nil || len(payload.MediaData) != 3 {
		t.Fatalf("ORD payload %+v %v", payload, err)
	}
	for i, media := range payload.MediaData {
		u, err := url.Parse(media.MediaURL)
		if err != nil {
			t.Fatal(err)
		}
		o, ok := e.files.Public(strings.TrimPrefix(u.Path, "/"))
		if !ok || sha256.Sum256(o.Data) != want[i] {
			t.Fatal("ORD files differ from preview/snapshot")
		}
	}
	o = e.getOrder(order.ID)
	pid := o.Placements[0].Placement.ID
	if _, err = e.placements.Accept(ctx, blogger, user, pid, ""); err != nil {
		t.Fatal(err)
	}
	e.clk.Set(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	if _, err = e.placements.Schedule(ctx, blogger, user, pid, placementdomain.ModeNow, nil, ""); err != nil {
		t.Fatal(err)
	}
	e.run()
	if !reflect.DeepEqual(capture.hashes, want) {
		t.Fatalf("MAX upload order/content differs: %v", capture.hashes)
	}
	if err = e.placements.Publish(ctx, pid, placementapp.JobRef{ID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if len(capture.hashes) != 3 {
		t.Fatal("repeat publish uploaded media again")
	}
	before, err := e.post.GetVersion(ctx, o.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := e.ord.Calls(ord.OpCreative)
	input.MediaIDs = []uuid.UUID{files[1], files[0], files[2]}
	if _, err = e.post.SaveDraft(e.ctx(), e.adv, e.advUser, c.ID, input); err != nil {
		t.Fatal(err)
	}
	e.run()
	if _, err = e.post.CreateVersion(e.ctx(), e.adv, c.ID, e.advUser, o.Order.Number); err != nil {
		t.Fatal(err)
	}
	if err = e.post.Delete(e.ctx(), e.adv, e.advUser, files[1]); err != nil {
		t.Fatal(err)
	}
	e.run()
	if err = e.svc.Sync(ctx, creative.Ref(), 1); err != nil {
		t.Fatal(err)
	}
	after, err := e.post.GetVersion(ctx, o.Version.ID)
	if err != nil || !reflect.DeepEqual(before, after) || e.ord.Calls(ord.OpCreative) != calls {
		t.Fatal("draft edit changed registered snapshot/erid")
	}
	current, _ := e.ord.Object(creative.ID.String())
	var currentPayload ord.Creative
	if current.Decode(&currentPayload) != nil || !reflect.DeepEqual(payload, currentPayload) {
		t.Fatal("registered ORD payload changed")
	}
}
