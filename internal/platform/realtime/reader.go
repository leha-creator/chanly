package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	PollInterval     = 250 * time.Millisecond
	batchLimit       = 1024
	journalRetention = 24 * time.Hour
)

// Reader uses short repeatable-read snapshots, never a connection per browser.
// A sequence ID is NOT a commit cursor: older IDs may still be uncommitted.
// Retain/deduplicate transactions >= the PREVIOUS snapshot xmin, in addition to
// reading new IDs. This also catches commits arriving below the ID high-water mark.
type Reader struct {
	pool     *pgxpool.Pool
	hub      *Hub
	log      *slog.Logger
	cursor   int64
	horizon  uint64
	epoch    int64
	seen     map[int64]uint64
	lastPoll time.Time
}

func NewReader(pool *pgxpool.Pool, hub *Hub, log *slog.Logger) *Reader {
	hub.creationBoundary = func(ctx context.Context) (time.Time, error) {
		var at time.Time
		err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at)
		return at, err
	}
	return &Reader{pool: pool, hub: hub, log: log, seen: make(map[int64]uint64)}
}

func (r *Reader) snapshot(ctx context.Context, reset bool) (overflow bool, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var horizonText string
	var high int64
	var epoch int64
	var prunedID int64
	var prunedXIDText string
	if err = tx.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot())::text,coalesce(max(id),0),
		coalesce((SELECT generation FROM realtime_epoch WHERE singleton),0),
		coalesce((SELECT max_pruned_id FROM realtime_epoch WHERE singleton),0),
		coalesce((SELECT max_pruned_transaction::text FROM realtime_epoch WHERE singleton),'0') FROM realtime_event`).Scan(&horizonText, &high, &epoch, &prunedID, &prunedXIDText); err != nil {
		return false, err
	}
	horizon, err := strconv.ParseUint(horizonText, 10, 64)
	if err != nil {
		return false, err
	}
	prunedXID, err := strconv.ParseUint(prunedXIDText, 10, 64)
	if err != nil {
		return false, err
	}
	if reset {
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		r.cursor = high
		r.horizon = horizon
		r.epoch = epoch
		clear(r.seen)
		r.lastPoll = time.Now()
		return false, nil
	}
	// Routine retention must not disconnect readers that demonstrably consumed
	// every pruned row. Both ID and XID frontiers are needed for late commits.
	if epoch != r.epoch && (prunedID > r.cursor || prunedXID >= r.horizon) {
		return true, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,created_at,transaction_id::text,scope,audience_id,topic,object_id,order_id,campaign_id,channel_id
		FROM realtime_event WHERE id > $1 OR transaction_id >= $2::text::xid8 ORDER BY id LIMIT $3`, r.cursor, strconv.FormatUint(r.horizon, 10), batchLimit+1)
	if err != nil {
		return false, err
	}
	var records []Record
	for rows.Next() {
		var e Record
		var xid string
		e.Event.Version = 1
		if err = rows.Scan(&e.ID, &e.CreatedAt, &xid, &e.Scope, &e.AudienceID, &e.Event.Topic, &e.Event.ObjectID, &e.Event.OrderID, &e.Event.CampaignID, &e.Event.ChannelID); err != nil {
			break
		}
		e.Transaction, err = strconv.ParseUint(xid, 10, 64)
		if err != nil {
			break
		}
		records = append(records, e)
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if len(records) > batchLimit || len(r.seen)+len(records) > batchLimit*2 || time.Since(r.lastPoll) > journalRetention/2 {
		return true, nil
	}
	for _, e := range records {
		if _, ok := r.seen[e.ID]; !ok {
			r.hub.Publish(e)
		}
		r.seen[e.ID] = e.Transaction
	}
	for id, xid := range r.seen {
		if xid < horizon {
			delete(r.seen, id)
		}
	}
	r.cursor = high
	r.horizon = horizon
	r.epoch = epoch
	r.lastPoll = time.Now()
	return false, nil
}

// Run closes all affected streams on any read failure, overflow or shutdown.
// Reconnecting clients can subscribe only AFTER a fresh snapshot is committed.
func (r *Reader) Run(ctx context.Context) {
	defer r.hub.Close()
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	clean := time.NewTicker(time.Minute)
	defer clean.Stop()
	reset := true
	for {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		overflow, err := r.snapshot(qctx, reset)
		cancel()
		if err != nil || overflow {
			r.hub.setHealthy(false)
			reset = true
			reason := "overflow"
			if err != nil {
				reason = "database"
			}
			r.log.WarnContext(ctx, "realtime reader interrupted", "reason", reason)
		} else if reset {
			reset = false
			r.hub.setHealthy(true)
			r.log.InfoContext(ctx, "realtime reader ready")
		}
		select {
		case <-ctx.Done():
			return
		case <-clean.C:
			qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := r.pool.Exec(qctx, `WITH removed AS (
				DELETE FROM realtime_event WHERE id IN (SELECT id FROM realtime_event WHERE created_at < clock_timestamp()-interval '24 hours' LIMIT 10000)
				RETURNING id,transaction_id
			) INSERT INTO realtime_epoch(singleton,generation,max_pruned_id,max_pruned_transaction)
			SELECT true,1,max(id),max(transaction_id::text::numeric)::text::xid8 FROM removed HAVING count(*)>0
			ON CONFLICT(singleton) DO UPDATE SET generation=realtime_epoch.generation+1,
			max_pruned_id=greatest(realtime_epoch.max_pruned_id,excluded.max_pruned_id),
			max_pruned_transaction=greatest(realtime_epoch.max_pruned_transaction,excluded.max_pruned_transaction)`)
			cancel()
			if err != nil {
				r.log.WarnContext(ctx, "realtime journal cleanup failed", "reason", fmt.Sprintf("%T", err))
			}
		case <-tick.C:
		}
	}
}
