package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// runDevChannelMetrics queues the normal refresh immediately, independently of
// the daily scheduler's dedupe key. runDev enforces development/test only.
func runDevChannelMetrics(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.DevChannelMetrics"
	fs := flag.NewFlagSet("dev channel-metrics", flag.ContinueOnError)
	fs.SetOutput(out)
	channel := fs.String("channel", "", "internal channel UUID")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	id, err := uuid.Parse(*channel)
	if err != nil || id == uuid.Nil {
		return errUsage
	}
	log.DebugContext(ctx, "manual channel metrics requested", logx.KeyOp, op, "channel_id", id)
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeDev, pool, log, nil)
	if err != nil {
		return err
	}
	queue := job.NewQueue(pool, in.clock, log)
	store := channelpg.New(pool, log)
	key := "manual_channel_metrics:" + id.String() + ":" + uuid.NewString()
	err = tx.NewRunner(pool, log).InTx(ctx, func(ctx context.Context) error {
		c, err := store.Channels().GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if c.Status != domain.StatusModeration && c.Status != domain.StatusActive && c.Status != domain.StatusSuspended {
			return fmt.Errorf("channel %s: metrics unavailable in status %s", id, c.Status)
		}
		now := in.clock.Now()
		return queue.Enqueue(ctx, job.Job{
			Kind: channelapp.KindRefreshChannelMetrics, DedupeKey: key,
			Payload: channelapp.MetricsPayload{ChannelID: id, Date: domain.MetricsDate(now).Format(time.DateOnly)},
			RunAt:   now, MaxAttempts: channelapp.RefreshMetricsMaxAttempts,
		})
	})
	if err != nil {
		log.ErrorContext(ctx, "manual channel metrics failed", logx.KeyOp, op, "channel_id", id, logx.KeyError, err)
		return err
	}
	log.InfoContext(ctx, "manual channel metrics queued", logx.KeyOp, op, "channel_id", id, "dedupe_key", key)
	fmt.Fprintf(out, "channel %s metrics queued (%s); a running worker will refresh today's snapshot\n", id, key)
	return nil
}
