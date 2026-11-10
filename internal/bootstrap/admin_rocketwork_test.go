package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRocketWorkRequestDoesNotAcceptSecretsOrTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"token":"private-canary"}`, `{"action":"invite"} {"action":"pay"}`, `{"names":{"first_name":"private-canary"},"unexpected":true}`} {
		file := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readRocketWorkAdminRequest(file)
		if err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatal("invalid request accepted or PII exposed")
		}
	}
}
