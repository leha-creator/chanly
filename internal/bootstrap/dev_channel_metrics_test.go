package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestDevChannelMetricsValidation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, args := range [][]string{
		{"channel-metrics"},
		{"channel-metrics", "--channel", "bad-id"},
		{"channel-metrics", "--channel", "00000000-0000-0000-0000-000000000000"},
		{"channel-metrics", "--channel", "bdb3a874-a9a3-4d77-b26e-17a74e0dc544", "extra"},
	} {
		if err := runDev(context.Background(), Config{Env: EnvTest}, log, args, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("args %v: got %v, want usage error", args, err)
		}
	}
	if err := runDev(context.Background(), Config{Env: EnvProduction}, log,
		[]string{"channel-metrics", "--channel", "bdb3a874-a9a3-4d77-b26e-17a74e0dc544"}, io.Discard); !errors.Is(err, errDevForbidden) {
		t.Fatalf("production: got %v, want dev forbidden", err)
	}
}
