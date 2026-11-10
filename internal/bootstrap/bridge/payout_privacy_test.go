package bridge

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestUnknownProviderStatusDoesNotLogUntrustedText(t *testing.T) {
	var out bytes.Buffer
	p := PayoutProvider{Log: slog.New(slog.NewJSONHandler(&out, nil))}
	p.unknown(context.Background(), "payout", "17", "private-provider-canary")
	if strings.Contains(out.String(), "private-provider-canary") || !strings.Contains(out.String(), `"status":"unknown"`) {
		t.Fatal("unknown provider text leaked or diagnostic lost")
	}
}
