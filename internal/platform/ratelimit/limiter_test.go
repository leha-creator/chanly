package ratelimit_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var (
	start  = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	global = ratelimit.Bucket{Key: "max:global", Rate: 25, Burst: 25}
	chat   = ratelimit.Bucket{Key: "max:chat:42", Rate: 1, Burst: 1}
)

var errWouldWait = errors.New("would wait")

// noWait — Sleep, который не ждёт: Wait возвращает ошибку вместо ожидания.
func noWait(context.Context, time.Duration) error { return errWouldWait }

func TestGlobalLimit(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	var slept []time.Duration
	l := ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: func(_ context.Context, w time.Duration) error {
		slept = append(slept, w)
		clk.Advance(w)
		return nil
	}})
	ctx := context.Background()
	for i := range 25 {
		if err := l.Wait(ctx, global, ratelimit.PriorityPublish); err != nil {
			t.Fatal(i, err)
		}
	}
	if len(slept) != 0 {
		t.Fatalf("waited within the first 25 requests: %v", slept)
	}
	if err := l.Wait(ctx, global, ratelimit.PriorityPublish); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 40*time.Millisecond {
		t.Fatalf("26th request waited %v, want 40ms", slept)
	}
}

func TestPerChatLimit(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	l := ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: noWait})
	ctx := context.Background()
	if err := l.Wait(ctx, chat, ratelimit.PriorityPublish); err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(ctx, chat, ratelimit.PriorityPublish); !errors.Is(err, errWouldWait) {
		t.Fatalf("second message in the same second: %v", err)
	}
	clk.Advance(time.Second)
	if err := l.Wait(ctx, chat, ratelimit.PriorityPublish); err != nil {
		t.Fatal(err)
	}
}

func TestLowPriorityKeepsReserve(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	l := ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: noWait})
	ctx := context.Background()
	got := 0
	for {
		if err := l.Wait(ctx, global, ratelimit.PriorityMetrics); err != nil {
			break
		}
		got++
	}
	// Метрики оставляют 10 токенов: берут 15 из 25.
	if got != 15 {
		t.Fatalf("metrics took %d tokens, want 15", got)
	}
	for i := range 10 {
		if err := l.Wait(ctx, global, ratelimit.PriorityPublish); err != nil {
			t.Fatalf("publish %d blocked by reserve: %v", i, err)
		}
	}
}

func TestLowPriorityBusyAfterMaxWait(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	l := ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: func(_ context.Context, w time.Duration) error {
		clk.Advance(w)
		return nil
	}})
	ctx := context.Background()
	slow := ratelimit.Bucket{Key: "max:chat:slow", Rate: 1.0 / 60, Burst: 1} // токен раз в минуту
	if err := l.Wait(ctx, slow, ratelimit.PriorityReply); err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(ctx, slow, ratelimit.PriorityReply); !errors.Is(err, ratelimit.ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}

func TestSharedAcrossInstances(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	limiters := []*ratelimit.Limiter{
		ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: noWait}),
		ratelimit.New(d.Pool, clk, logx.Discard(), ratelimit.Options{Sleep: noWait}),
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiters[i%2].Wait(context.Background(), global, ratelimit.PriorityPublish) == nil {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 25 {
		t.Fatalf("granted %d tokens in one instant, want 25", granted.Load())
	}
}
