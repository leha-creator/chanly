package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/clock"
)

type maxCheckRun struct {
	Version     int       `json:"version"`
	RunID       string    `json:"run_id"`
	BotID       int64     `json:"bot_id"`
	ChatID      int64     `json:"chat_id"`
	CreatedAt   time.Time `json:"created_at"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Status      string    `json:"status"`
	MessageID   string    `json:"message_id,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// A per-run lock prevents two CLI processes from issuing the same mutation.
// A crash leaves the lock in place; a human must establish that no process is
// running before removing it. The pending state never authorizes a second POST.
func openMAXCheckRun(dir string, create bool, botID, chatID int64, now time.Time) (*maxCheckRun, func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	lock := filepath.Join(dir, "run.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lock)
		return nil, nil, err
	}
	unlock := func() { _ = os.Remove(lock) }
	fail := func(err error) (*maxCheckRun, func(), error) { unlock(); return nil, nil, err }
	path := filepath.Join(dir, "state.json")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		s := &maxCheckRun{Version: 1, RunID: newCheckRunID(), BotID: botID, ChatID: chatID, CreatedAt: now, Status: "prepared"}
		if err := saveMAXCheckRun(dir, s); err != nil {
			return fail(err)
		}
		return s, unlock, nil
	}
	if err != nil {
		return fail(err)
	}
	var s maxCheckRun
	if err := json.Unmarshal(body, &s); err != nil {
		return fail(err)
	}
	if _, err := uuid.Parse(s.RunID); err != nil || s.Version != 1 || s.BotID != botID || s.ChatID != chatID ||
		s.CreatedAt.IsZero() || s.CreatedAt.After(now) {
		return fail(errors.New("invalid state or run belongs to another bot/channel"))
	}
	return &s, unlock, nil
}

func saveMAXCheckRun(dir string, s *maxCheckRun) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, "state.json"))
}

func advanceMAXCheck(ctx context.Context, api maxAPI, clk clock.Clock, log *slog.Logger, o integrationCheckOptions, s *maxCheckRun) error {
	switch o.scenario {
	case "publish":
		if s.Status != "prepared" {
			log.WarnContext(ctx, "publication refused; existing run must be observed or recovered", "state", s.Status)
			return errors.New("publication already attempted")
		}
		s.Status = "publishing"
		if err := saveMAXCheckRun(o.runDir, s); err != nil {
			return err
		}
		log.InfoContext(ctx, "publishing dedicated test post", "chat_id", s.ChatID)
		post, err := api.SendChannelPost(ctx, s.ChatID, "Техническая проверка MAX. Не реклама.\nmaxim-check:"+s.RunID, nil,
			max.Recovery{SenderID: s.BotID, Since: s.CreatedAt, Needle: "maxim-check:" + s.RunID})
		if err != nil {
			return err
		} // state remains publishing even for ambiguous errors
		if post.ID == "" {
			return errors.New("MAX returned no message ID")
		}
		s.Status, s.MessageID, s.ContentHash = "published", post.ID, post.ContentHash
		// Start the 72h interval after the successful response, conservatively.
		s.PublishedAt = clk.Now()
		return saveMAXCheckRun(o.runDir, s)
	case "recover":
		if s.Status != "publishing" {
			return errors.New("only an uncertain publication can be recovered")
		}
		found, err := findMAXCheckPost(ctx, api, clk, s)
		if err != nil {
			return err
		}
		if found == nil || found.MID == "" {
			log.WarnContext(ctx, "test post not found; no repost authorized, manual reconciliation required")
			return errors.New("publication outcome remains unknown")
		}
		s.Status, s.MessageID, s.ContentHash = "published", found.MID, found.ContentHash
		// Conservative: recovery starts a fresh full interval, never shortens it.
		s.PublishedAt = clk.Now()
		log.InfoContext(ctx, "test post recovered without reposting")
		return saveMAXCheckRun(o.runDir, s)
	case "observe", "remove":
		return observeOrRemoveMAXCheck(ctx, api, clk, log, o, s)
	default:
		return errUsage
	}
}

func observeOrRemoveMAXCheck(ctx context.Context, api maxAPI, clk clock.Clock, log *slog.Logger, o integrationCheckOptions, s *maxCheckRun) error {
	if (s.Status != "published" && s.Status != "removing" && s.Status != "removed") || s.MessageID == "" || s.PublishedAt.IsZero() {
		return errors.New("run has no confirmed publication")
	}
	message, err := api.GetMessage(ctx, s.MessageID)
	if errors.Is(err, max.ErrMessageNotFound) {
		if s.Status == "removed" {
			log.InfoContext(ctx, "previously removed test post is absent")
			return nil
		}
		log.WarnContext(ctx, "test post absent; deletion by this run is not proven", "state", s.Status)
		return errors.New("post absent before confirmed deletion")
	}
	if err != nil {
		return err
	}
	if message.MID != s.MessageID || (message.SenderID != 0 && message.SenderID != s.BotID) {
		return errors.New("stored message identity is not confirmed")
	}
	// Channel posts may omit sender even when published by this bot. Verify
	// the persisted ID and unique run marker through the dedicated channel.
	if message.SenderID == 0 {
		found, err := findMAXCheckPost(ctx, api, clk, s)
		if err != nil {
			return err
		}
		if found == nil || found.MID != s.MessageID {
			return errors.New("senderless test post ownership could not be verified")
		}
	}
	hashMatches := message.ContentHash == s.ContentHash
	log.InfoContext(ctx, "test post observed", "hash_matches", hashMatches, "views", message.Views,
		"age_seconds", int64(clk.Now().Sub(s.PublishedAt).Seconds()))
	if o.scenario == "observe" {
		if !hashMatches {
			return errors.New("post content differs from publication response")
		}
		return nil
	}
	if s.Status == "removed" {
		return errors.New("post reappeared after deletion; manual reconciliation required")
	}
	if clk.Now().Sub(s.PublishedAt) < 72*time.Hour {
		log.WarnContext(ctx, "deletion deferred until real 72h interval has elapsed", "not_before", s.PublishedAt.Add(72*time.Hour))
		return errors.New("72h interval not elapsed")
	}
	// Confirm that the stored ID still belongs to the dedicated channel/run.
	found, err := findMAXCheckPost(ctx, api, clk, s)
	if err != nil {
		return err
	}
	if found == nil || found.MID != s.MessageID {
		return errors.New("test post ownership could not be verified")
	}
	s.Status = "removing"
	if err := saveMAXCheckRun(o.runDir, s); err != nil {
		return err
	}
	log.InfoContext(ctx, "removing dedicated test post after 72h")
	if err := api.DeleteMessage(ctx, s.ChatID, s.MessageID); err != nil {
		return err
	}
	if _, err := api.GetMessage(ctx, s.MessageID); !errors.Is(err, max.ErrMessageNotFound) {
		return errors.New("deletion absence not yet confirmed")
	}
	s.Status = "removed"
	if err := saveMAXCheckRun(o.runDir, s); err != nil {
		return err
	}
	log.InfoContext(ctx, "72h removal check passed", "question", "Q-07")
	return nil
}

func findMAXCheckPost(ctx context.Context, api maxAPI, clk clock.Clock, s *maxCheckRun) (*max.ChannelMessage, error) {
	for _, sender := range []int64{s.BotID, 0} {
		found, err := api.FindPost(ctx, s.ChatID, s.CreatedAt, clk.Now(), sender, "maxim-check:"+s.RunID)
		if err != nil {
			return nil, err
		}
		if found != nil && found.SenderID == sender {
			return found, nil
		}
	}
	return nil, nil
}
