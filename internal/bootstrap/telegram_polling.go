package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/telegram"
	"github.com/leha-creator/chanly/internal/platform/inbound"
)

// One session lock elects a receiver across workers and both release slots.
// The offset advances only after durable event+job insertion. A restart can
// redeliver the last batch, which the recorder deduplicates by update_id.
const telegramReceiverLock int64 = 7424086019180029

func (m *modules) runTelegramPolling(ctx context.Context) error {
	client := telegramClient(m.cfg, m.in)
	recorder := inbound.NewRecorder(m.in.pool, m.txRunner, m.queue, m.in.clock, m.in.log)
	for ctx.Err() == nil {
		err := m.receiveTelegram(ctx, client, recorder)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			m.in.log.ErrorContext(ctx, "[FIX] telegram receiver retry", "error", err.Error())
		}
		if !telegramPause(ctx, 5*time.Second) {
			break
		}
	}
	return nil
}

func (m *modules) receiveTelegram(ctx context.Context, client *telegram.Client, recorder *inbound.Recorder) error {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	connection, err := pgx.ConnectConfig(connectCtx, m.in.pool.Config().ConnConfig.Copy())
	cancel()
	if err != nil {
		return err
	}
	// Use a separate session so leadership never consumes the last pool slot.
	// Closing it releases the lock even on an uncertain network failure.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = connection.Close(closeCtx)
	}()
	var leader bool
	if err := connection.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", telegramReceiverLock).Scan(&leader); err != nil {
		return err
	}
	if !leader {
		return nil
	}
	if err := client.DeleteWebhook(ctx); err != nil {
		return err
	}
	m.in.log.InfoContext(ctx, "[FIX] telegram polling receiver started")
	var offset int64
	for ctx.Err() == nil {
		if err := connection.Ping(ctx); err != nil {
			return err
		}
		updates, err := client.GetUpdates(ctx, offset)
		if err != nil {
			return err
		}
		next, err := recordTelegramBatch(ctx, m.cfg, recorder, updates, offset)
		if err != nil {
			return err
		}
		offset = next
		if !telegramPause(ctx, 250*time.Millisecond) {
			break
		}
	}
	return nil
}

type telegramRecorder interface {
	Record(context.Context, inbound.Event) (bool, error)
}

func recordTelegramBatch(ctx context.Context, cfg Config, recorder telegramRecorder, updates []json.RawMessage, offset int64) (int64, error) {
	for _, raw := range updates {
		id, start, ok, err := telegram.ParseStart(raw, cfg.Telegram.BotName, func(token string) string {
			return accountdomain.HashTelegramBotToken([]byte(cfg.AuthCodePepper), token)
		})
		if err != nil {
			return offset, err
		}
		if id < offset {
			continue
		}
		if ok {
			payload, _ := json.Marshal(start)
			if _, err := recorder.Record(ctx, inbound.Event{Source: "telegram", ExternalKey: strconv.FormatInt(id, 10), EventType: "bot_started", Payload: payload}); err != nil {
				return offset, fmt.Errorf("record telegram update: %w", err)
			}
		}
		offset = id + 1
	}
	return offset, nil
}

func telegramPause(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
