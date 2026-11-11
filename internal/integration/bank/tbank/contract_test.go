package tbank

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

// Validate the fixture against the supplied bank schema, independently of the
// hand-written Go response types. Load only the reachable statement schemas.
func TestFixtureMatchesSuppliedStatementSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/tbank-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	all := document["components"].(map[string]any)["schemas"].(map[string]any)
	selected := map[string]any{}
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if _, exists := selected[name]; !exists {
					schema, exists := all[name]
					if !exists {
						t.Fatalf("schema missing: %s", name)
					}
					selected[name] = schema
					visit(schema)
				}
			}
			for key, child := range v {
				if key != "$ref" {
					visit(child)
				}
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	selected["GetOperationsResult868670981"] = all["GetOperationsResult868670981"]
	visit(selected["GetOperationsResult868670981"])
	minimal, _ := json.Marshal(map[string]any{"openapi": "3.0.3", "info": map[string]any{"title": "TBank statement", "version": "1.0"}, "paths": map[string]any{}, "components": map[string]any{"schemas": selected}})
	spec, err := openapi3.NewLoader().LoadFromData(minimal)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"testdata/statement.json", "testdata/sandbox-static-anonymized.json"}
	if responseFile := os.Getenv("TBANK_CONTRACT_RESPONSE_FILE"); responseFile != "" {
		files = append(files, responseFile)
	}
	for _, name := range files {
		t.Run(filepath.Base(name), func(t *testing.T) {
			body, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var response any
			if err = json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if err = spec.Components.Schemas["GetOperationsResult868670981"].Value.VisitJSON(response); err != nil {
				t.Fatalf("statement response fails supplied schema (%T)", err)
			}
		})
	}
}
