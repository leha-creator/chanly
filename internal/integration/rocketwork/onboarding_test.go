package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestOnboardingWireContractAndNoInlineReplay(t *testing.T) {
	validate := contractValidator(t)
	posts := 0
	status := 200
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errors":[]}`))
			return
		}
		posts++
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("undocumented header")
		}
		var body any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		pointer := "/paths/" + strings.ReplaceAll(r.URL.Path, "/", "~1") + "/post/requestBody/content/application~1json/schema"
		if r.URL.Path == "/api/executors" {
			pointer = "/components/schemas/ExecutorCreateParams/anyOf/8"
		}
		if err := validate(pointer, body); err != nil {
			t.Errorf("request: %v", err)
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path == "/api/executors/invite" {
			writeJSON(w, 200, map[string]string{"status": "invitation_sent"})
			return
		}
		e := syntheticExecutor()
		e["employment_kind"] = "entrepreneur"
		writeJSON(w, 200, map[string]any{"executor": e})
	})
	ctx := context.Background()
	if result, err := c.InviteExecutor(ctx, rocketwork.InvitationRequest{Phone: "+79000000000"}); err != nil || result != "invitation_sent" {
		t.Fatalf("invite: %s %v", result, err)
	}
	if e, err := c.RegisterEntrepreneur(ctx, rocketwork.EntrepreneurRequest{INN: "000000000000", Phone: "+79000000000", FirstName: "Synthetic", LastName: "Fixture"}); err != nil || e.ID != 7 {
		t.Fatalf("register: %+v %v", e, err)
	}
	for _, code := range []int{429, 500, 503} {
		status = code
		before := posts
		if _, err := c.InviteExecutor(ctx, rocketwork.InvitationRequest{Phone: "+79000000000"}); !errors.Is(err, rocketwork.ErrUncertain) || posts != before+1 {
			t.Fatalf("write replay on %d: posts=%d err=%v", code, posts-before, err)
		}
	}
}
