package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/telegram"
	"github.com/leha-creator/chanly/internal/notification/notificationtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/outbound"
)

type telegramCallerFunc func(context.Context, outbound.Call) (outbound.Response, error)

func (f telegramCallerFunc) Do(ctx context.Context, call outbound.Call) (outbound.Response, error) {
	return f(ctx, call)
}

func TestTelegramPollingElectsOneReceiverAndReleasesLeadership(t *testing.T) {
	a := newTestAppWith(t, telegramTestConfig())
	var starts atomic.Int32
	started := make(chan struct{}, 2)
	client := &telegram.Client{Outbound: telegramCallerFunc(func(ctx context.Context, call outbound.Call) (outbound.Response, error) {
		if call.Operation == "deleteWebhook" {
			starts.Add(1)
			return outbound.Response{Status: 200, Body: []byte(`{"ok":true,"result":true}`)}, nil
		}
		started <- struct{}{}
		<-ctx.Done()
		return outbound.Response{}, ctx.Err()
	})}
	for want := int32(1); want <= 2; want++ {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- a.mods.receiveTelegram(ctx, client, nil) }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("receiver did not start")
		}
		if err := a.mods.receiveTelegram(t.Context(), client, nil); err != nil {
			cancel()
			t.Fatal(err)
		}
		if starts.Load() != want {
			cancel()
			t.Fatal("two receivers held leadership")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("receiver did not stop")
		}
	}
}

type telegramRecorderFunc func(context.Context, inbound.Event) (bool, error)

func (f telegramRecorderFunc) Record(ctx context.Context, e inbound.Event) (bool, error) {
	return f(ctx, e)
}

func TestTelegramPollingOffsetWaitsForDurableStorage(t *testing.T) {
	updates := []json.RawMessage{
		json.RawMessage(`{"update_id":40,"message":{"text":"hello"}}`),
		json.RawMessage(`{"update_id":41,"message":{"text":"/start notify_` + strings.Repeat("x", 43) + `","from":{"id":9},"chat":{"id":9,"type":"private"}}}`),
	}
	failed := errors.New("database unavailable")
	next, err := recordTelegramBatch(t.Context(), telegramTestConfig(), telegramRecorderFunc(func(context.Context, inbound.Event) (bool, error) { return false, failed }), updates, 40)
	if next != 41 || !errors.Is(err, failed) {
		t.Fatalf("unpersisted command acknowledged: offset=%d error=%v", next, err)
	}
	count := 0
	next, err = recordTelegramBatch(t.Context(), telegramTestConfig(), telegramRecorderFunc(func(_ context.Context, e inbound.Event) (bool, error) {
		count++
		if e.ExternalKey != "41" || strings.Contains(string(e.Payload), strings.Repeat("x", 43)) {
			t.Fatal("unsafe stored event")
		}
		return true, nil // A replay is already durably stored.
	}), updates, next)
	if next != 42 || err != nil || count != 1 {
		t.Fatalf("retry offset=%d count=%d error=%v", next, count, err)
	}
}

func TestTelegramPolledStartConnectsAndReplies(t *testing.T) {
	a := newTestAppWith(t, telegramTestConfig())
	c := a.newAdvertiser("telegram-poll@example.test")
	delivery := &notificationtest.Delivery{}
	a.mods.notification.Telegram = delivery
	v := createTelegramBot(t, c)
	// Use the same event recorder and queue as the actual polling receiver.
	recorder := inbound.NewRecorder(a.mods.in.pool, a.mods.txRunner, a.mods.queue, a.mods.in.clock, a.mods.in.log)
	link := strings.Split(v.BotUrl, "?start=")[1]
	raw := json.RawMessage(fmt.Sprintf(`{"update_id":71,"message":{"text":%q,"from":{"id":8101},"chat":{"id":8101,"type":"private"}}}`, "/start "+link))
	next, err := recordTelegramBatch(t.Context(), a.mods.cfg, recorder, []json.RawMessage{raw}, 0)
	if err != nil || next != 72 {
		t.Fatalf("offset=%d error=%v", next, err)
	}
	// A restart can redeliver the previous batch; it must not enqueue twice.
	if _, err := recordTelegramBatch(t.Context(), a.mods.cfg, recorder, []json.RawMessage{raw}, 0); err != nil {
		t.Fatal(err)
	}
	delivery.FailMAX(errors.New("temporary Telegram failure"))
	a.settle()
	if !telegramConnected(t, c) || len(delivery.Messages()) != 0 {
		t.Fatal("connection was not saved before retrying the reply")
	}
	delivery.FailMAX(nil)
	a.clk.Advance(time.Minute)
	a.settle()
	if !telegramConnected(t, c) || len(delivery.Messages()) != 1 {
		t.Fatal("polled command did not connect and reply exactly once")
	}
	bare := json.RawMessage(`{"update_id":72,"message":{"text":"/start","from":{"id":8102},"chat":{"id":8102,"type":"private"}}}`)
	if _, err := recordTelegramBatch(t.Context(), a.mods.cfg, recorder, []json.RawMessage{bare}, next); err != nil {
		t.Fatal(err)
	}
	a.settle()
	if len(delivery.Messages()) != 2 || !strings.Contains(fmt.Sprint(delivery.Messages()[1]), "новой ссылке") {
		t.Fatal("bare start did not receive instructions")
	}
}
