package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestLegalCommand(t *testing.T) {
	a := newTestApp(t)
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	file := filepath.Join(t.TempDir(), "offer.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.7 offer"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for range 2 {
		if err := runLegal(t.Context(), cfg, logx.Discard(), []string{"publish", "--kind", "advertiser_offer", "--file", file}, &out); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), "advertiser_offer version 2") {
		t.Fatalf("publish output: %s", out.String())
	}
	out.Reset()
	if err := runLegal(t.Context(), cfg, logx.Discard(), []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "advertiser_offer  2") {
		t.Fatalf("list output:\n%s", out.String())
	}
	if err := runLegal(t.Context(), cfg, logx.Discard(), []string{"publish", "--kind", "unknown", "--file", file}, &out); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := runLegal(t.Context(), cfg, logx.Discard(), []string{"publish"}, &out); err != errUsage {
		t.Fatalf("missing flags: %v", err)
	}
}
