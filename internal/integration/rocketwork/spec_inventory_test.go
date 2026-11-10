package rocketwork

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The original is deliberately read from the repository, not a rewritten copy.
// Hash pinning makes schema drift a reviewable change, including in CI.
func loadPinnedSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../roketwork_openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	const want = "cb6e842ac2f683be2e942ea92f5f20919cfcad8e4512b39af4d0d706414694ff"
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
		t.Fatal("RocketWork contract hash changed; review coverage and schema exceptions")
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["openapi"] != "3.0.1" || spec["info"].(map[string]any)["version"] != "1.0" {
		t.Fatal("contract version changed")
	}
	return spec
}

func TestOpenAPIOperationInventory(t *testing.T) {
	spec := loadPinnedSpec(t)
	var inventory struct {
		Operations []struct {
			Method, Path, Pointer, Request, Money, Effect, Reconciliation, Coverage, Reason string
			Task                                                                            int
			Responses                                                                       map[string]string
		}
	}
	raw, err := os.ReadFile("testdata/coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range inventory.Operations {
		key := strings.ToLower(row.Method) + " " + row.Path
		if seen[key] {
			t.Fatalf("duplicate operation %s", key)
		}
		seen[key] = true
		path, ok := spec["paths"].(map[string]any)[row.Path].(map[string]any)
		if !ok {
			t.Fatalf("unknown operation %s", key)
		}
		op, ok := path[strings.ToLower(row.Method)].(map[string]any)
		if !ok {
			t.Fatalf("unknown method %s", key)
		}
		pointer := "/paths/" + strings.ReplaceAll(row.Path, "/", "~1") + "/" + strings.ToLower(row.Method)
		if row.Pointer != pointer || row.Request == "" || row.Money == "" || row.Effect == "" || row.Reconciliation == "" || row.Reason == "" || row.Coverage == "" || row.Task < 1 || row.Task > 18 {
			t.Fatalf("incomplete inventory %s", key)
		}
		responses := op["responses"].(map[string]any)
		if len(responses) != len(row.Responses) {
			t.Fatalf("response inventory changed: %s", key)
		}
		for code := range responses {
			if row.Responses[code] != pointer+"/responses/"+code {
				t.Fatalf("missing response %s %s", key, code)
			}
		}
	}
	for path, item := range spec["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			switch method {
			case "get", "post", "put", "patch", "delete", "head", "options":
				if !seen[method+" "+path] {
					t.Errorf("unclassified operation %s %s", method, path)
				}
			}
		}
	}
	t.Logf("classified %d operations against pinned OpenAPI", len(seen))
}
