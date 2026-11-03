package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

// imageCampaign — кампания 20–22 октября с постом и n изображениями; производные построены.
func (e *env) imageCampaign(n int) uuid.UUID {
	e.t.Helper()
	id := e.campaign("С картинками")
	var media []uuid.UUID
	for i := range n {
		v, err := e.post.Upload(e.ctx(), e.adv, e.advUser, "photo.jpg", imagingtest.JPEG(800+i, 600, 0))
		if err != nil {
			e.t.Fatal(err)
		}
		media = append(media, v.File.ID)
	}
	e.runJobs()
	carrier := postdomain.CarrierText
	doc := postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{{Text: "Суп за 15 минут"}}}}}
	if _, err := e.post.SaveDraft(e.ctx(), e.adv, e.advUser, id, postapp.DraftInput{Doc: doc, MediaIDs: media, TokenCarrier: &carrier}); err != nil {
		e.t.Fatal(err)
	}
	e.runJobs()
	return id
}

// scheduledNow — размещение в scheduled: блогер нажал «Опубликовать сейчас» 20 октября в 11:30 МСК.
func (e *env) scheduledNow(images int) (pid uuid.UUID, chatID int64) {
	e.t.Helper()
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	e.addToCart(ch)
	campaign := e.campaign("Кампания")
	if images > 0 {
		campaign = e.imageCampaign(images)
	}
	o, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
	if err != nil {
		e.t.Fatal(err)
	}
	view := e.getOrder(o.ID)
	e.ready(view)
	pid = view.Placements[0].Placement.ID
	if _, err := e.svc.Accept(e.ctx(), blogger, user, pid, ""); err != nil {
		e.t.Fatal(err)
	}
	e.clk.Set(msk(20, 11, 30))
	if _, err := e.svc.Schedule(e.ctx(), blogger, user, pid, domain.ModeNow, nil, ""); err != nil {
		e.t.Fatal(err)
	}
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT max_chat_id FROM channel WHERE id = $1`, ch).Scan(&chatID); err != nil {
		e.t.Fatal(err)
	}
	return pid, chatID
}

func (e *env) publication(pid uuid.UUID) domain.Publication {
	e.t.Helper()
	pub, err := e.store.Publications().GetByPlacement(context.Background(), pid)
	if err != nil {
		e.t.Fatal(err)
	}
	return pub
}

func TestPublishWithImages(t *testing.T) {
	e := newEnv(t)
	pid, chatID := e.scheduledNow(2)
	e.clk.Advance(7 * time.Second)
	e.runJobs()

	p := e.placement(pid)
	posts := e.max.Posts()
	if p.Status != domain.StatusPublished || len(posts) != 1 || posts[0].ChatID != chatID || len(posts[0].ImageTokens) != 2 {
		t.Fatalf("published %s, posts %+v", p.Status, posts)
	}
	view, _ := e.post.GetVersion(context.Background(), p.PostVersionID)
	if posts[0].Text != view.Version.FinalText || !strings.Contains(posts[0].Text, "erid: ") {
		t.Fatalf("text %q", posts[0].Text)
	}
	// remove_at — от фактического времени публикации (S-21 шаг 4).
	if !p.PublishedAt.Equal(msk(20, 11, 30).Add(7*time.Second)) || !p.RemoveAt.Equal(p.PublishedAt.Add(24*time.Hour)) ||
		!e.jobRunAt(domain.RemoveKey(pid)).Equal(*p.RemoveAt) {
		t.Fatalf("published_at %v remove_at %v", p.PublishedAt, p.RemoveAt)
	}
	pub := e.publication(pid)
	if pub.MaxMessageID != posts[0].ID || pub.ContentHash != posts[0].Hash || pub.URL == "" {
		t.Fatalf("publication %+v", pub)
	}
	if e.notices.Count(domain.NoticePublished) != 2 {
		t.Fatalf("published notices %v", e.notices.Templates())
	}
	// Повтор задания ничего не меняет (03 §11).
	if err := e.svc.Publish(context.Background(), pid, app.JobRef{ID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if len(e.max.Posts()) != 1 || e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND transition = 'P-08'`, pid) != 1 {
		t.Fatal("repeat published twice")
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND transition = 'P-08' AND job_id IS NOT NULL`, pid) != 1 {
		t.Fatal("history without job source")
	}
	if strings.Contains(e.logs.String(), "Суп за 15 минут") || strings.Contains(e.logs.String(), "token.fake") {
		t.Fatal("post text or upload token written to the log")
	}
}

func TestPublishPreconditions(t *testing.T) {
	t.Run("channel suspended", func(t *testing.T) {
		e := newEnv(t)
		pid, _ := e.scheduledNow(0)
		if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'suspended', status_reason = 'missing_rights'
			WHERE id = (SELECT channel_id FROM placement WHERE id = $1)`, pid); err != nil {
			t.Fatal(err)
		}
		e.runJobs()
		p := e.placement(pid)
		if p.Status != domain.StatusFailed || p.StatusReason != domain.ReasonPreconditionFailed || len(e.max.Posts()) != 0 {
			t.Fatalf("status %s reason %s posts %d", p.Status, p.StatusReason, len(e.max.Posts()))
		}
		if e.holds.Active(pid) || e.jobStatus(domain.RemoveKey(pid)) != "cancelled" {
			t.Fatal("hold or removal job left")
		}
		if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND reason_text = 'channel_not_active'`, pid) != 1 {
			t.Fatal("reason text")
		}
		if r := e.alerts.reasons(); len(r) != 1 || r[0] != "placement_publish_failed" {
			t.Fatalf("alerts %v", r)
		}
	})
	t.Run("final text forged", func(t *testing.T) {
		e := newEnv(t)
		pid, _ := e.scheduledNow(0)
		ctx := context.Background()
		for _, stmt := range []string{
			`ALTER TABLE post_version DISABLE TRIGGER USER`,
			`UPDATE post_version SET final_text = final_text || ' подмена' WHERE id = (SELECT post_version_id FROM placement WHERE id = $1)`,
			`ALTER TABLE post_version ENABLE TRIGGER USER`,
		} {
			var err error
			if strings.HasPrefix(stmt, "UPDATE") {
				_, err = e.d.Owner.Exec(ctx, stmt, pid)
			} else {
				_, err = e.d.Owner.Exec(ctx, stmt)
			}
			if err != nil {
				t.Fatalf("forge final text: %v", err)
			}
		}
		e.runJobs()
		if p := e.placement(pid); p.Status != domain.StatusFailed || len(e.max.Posts()) != 0 {
			t.Fatalf("forged text: %s", p.Status)
		}
	})
}

func TestPublishRetriesUntilT03(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.scheduledNow(0)
	e.max.FailSendWith(errors.New("max: status 502"))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusScheduled {
		t.Fatalf("after first failure: %s", p.Status)
	}
	if at := e.jobRunAt(domain.PublishKey(pid)); !at.Equal(e.clk.Now().Add(30 * time.Second)) {
		t.Fatalf("retry at %v", at)
	}
	e.clk.Advance(29 * time.Minute)
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusScheduled {
		t.Fatalf("29 min: %s", p.Status)
	}
	e.clk.Advance(2 * time.Minute)
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusFailed || p.StatusReason != domain.ReasonPublishError || e.holds.Active(pid) {
		t.Fatalf("after T-03: %s %s", p.Status, p.StatusReason)
	}
	if len(e.logged("placement publish retry")) == 0 || len(e.logged("placement publish failed")) != 1 {
		t.Fatal("retry and failure not logged")
	}
}

func TestPublishRejectedFailsAtOnce(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.scheduledNow(0)
	e.max.FailSendWith(job.Permanent(errors.New("max: chat.denied: bot is not an admin")))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusFailed || p.StatusReason != domain.ReasonPublishError {
		t.Fatalf("4xx: %s %s", p.Status, p.StatusReason)
	}
}

func TestPublishUnknownOutcomeNoDuplicate(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.scheduledNow(0)
	e.max.FailSendAfterCreate(errors.New("max: timeout"))
	e.runJobs() // пост создан, но ответа нет — повтор через 30 с
	if p := e.placement(pid); p.Status != domain.StatusScheduled || len(e.max.Posts()) != 1 {
		t.Fatalf("after timeout: %s posts %d", p.Status, len(e.max.Posts()))
	}
	e.clk.Advance(31 * time.Second)
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusPublished || len(e.max.Posts()) != 1 {
		t.Fatalf("after retry: %s posts %d", p.Status, len(e.max.Posts()))
	}
	if len(e.logged("publication found after unknown outcome")) != 1 {
		t.Fatal("recovery not logged")
	}
	if e.publication(pid).MaxMessageID != e.max.Posts()[0].ID {
		t.Fatal("publication bound to another message")
	}
}

func TestPublishAfterDowntime(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.scheduledNow(0)
	e.clk.Advance(31 * time.Minute) // worker простаивал
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusFailed || len(e.max.Posts()) != 0 {
		t.Fatalf("after 31 min downtime: %s posts %d", p.Status, len(e.max.Posts()))
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND reason_text = 'downtime'`, pid) != 1 {
		t.Fatal("downtime reason")
	}

	e2 := newEnv(t)
	pid2, _ := e2.scheduledNow(0)
	e2.clk.Advance(29 * time.Minute)
	e2.runJobs()
	if p := e2.placement(pid2); p.Status != domain.StatusPublished {
		t.Fatalf("after 29 min downtime: %s", p.Status)
	}
}

func TestPublishTooManyImages(t *testing.T) {
	e := newEnv(t)
	e.svc.Config.Control.MaxImages = 1
	pid, _ := e.scheduledNow(2)
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusFailed {
		t.Fatalf("too many images: %s", p.Status)
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1 AND reason_text = 'too_many_images'`, pid) != 1 {
		t.Fatal("reason text")
	}
}
