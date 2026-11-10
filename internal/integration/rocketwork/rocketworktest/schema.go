package rocketworktest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Only these malformed property-local required flags are normalized. A new
// defect fails compilation until explicitly reviewed. No production DTO is
// used to construct the validator.
var requiredFixes = map[string]string{
	"/components/schemas/BankCardParams/anyOf/0":   "card_number",
	"/components/schemas/TaskCreateParams/allOf/0": "description",
	"/components/schemas/TaskCreateParams/allOf/1": "title",
}

func normalizeSchema(value any, pointer string) any {
	switch v := value.(type) {
	case map[string]any:
		if field, ok := requiredFixes[pointer]; ok {
			properties := v["properties"].(map[string]any)
			delete(properties[field].(map[string]any), "required")
			required, _ := v["required"].([]any)
			found := false
			for _, name := range required {
				found = found || name == field
			}
			if !found {
				required = append(required, field)
			}
			v["required"] = required
		}
		for k, x := range v {
			v[k] = normalizeSchema(x, pointer+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
		}
		// Translate OpenAPI nullable into JSON Schema, including nullable $ref
		// and allOf. The original schema remains an intact branch of anyOf.
		if v["nullable"] == true {
			delete(v, "nullable")
			return map[string]any{"anyOf": []any{v, map[string]any{"type": "null"}}}
		}
		return v
	case []any:
		for i, x := range v {
			v[i] = normalizeSchema(x, fmt.Sprintf("%s/%d", pointer, i))
		}
	}
	return value
}

func Validator(t testing.TB) func(string, any) error {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../../roketwork_openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "cb6e842ac2f683be2e942ea92f5f20919cfcad8e4512b39af4d0d706414694ff" {
		t.Fatal("contract hash mismatch")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	const source = "https://fixture.invalid/rocketwork.json"
	if err := c.AddResource(source, normalizeSchema(doc, "")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	return func(pointer string, body any) error {
		mu.Lock()
		defer mu.Unlock()
		schema, err := c.Compile(source + "#" + pointer)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return schema.Validate(v)
	}
}
