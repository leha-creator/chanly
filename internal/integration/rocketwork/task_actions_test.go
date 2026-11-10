package rocketwork_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestTaskActionsUseSeparatePatchWithoutReplay(t *testing.T) {
	for _, action := range []string{"complete", "sign_act", "pay", "cancel"} {
		t.Run(action, func(t *testing.T) {
			posts := 0
			code := 200
			state := map[string]string{"complete": "inwork", "sign_act": "accepting", "pay": "completed", "cancel": "draft"}[action]
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PATCH" {
					posts++
					if !strings.HasSuffix(r.URL.Path, "/"+action) || r.ContentLength > 0 {
						t.Error("invalid action request")
					}
					if code != 200 {
						w.WriteHeader(code)
						return
					}
				}
				writeJSON(w, 200, map[string]any{"task": map[string]any{"id": 7, "type": "OneTime", "external_id": "same", "amount_gross": "10.01", "status": state, "act_required": true, "signed_at": nil, "executor_signed_at": nil}})
			})
			call := map[string]func(context.Context, int64) (rocketwork.TaskSnapshot, error){"complete": c.CompleteTask, "sign_act": c.SignTaskAct, "pay": c.PayTask, "cancel": c.CancelTask}[action]
			if _, err := call(context.Background(), 7); err != nil || posts != 1 {
				t.Fatalf("action: %d %v", posts, err)
			}
			for _, status := range []int{429, 500, 503} {
				code = status
				before := posts
				if _, err := call(context.Background(), 7); !errors.Is(err, rocketwork.ErrUncertain) || posts != before+1 {
					t.Fatalf("replay: %d %v", posts-before, err)
				}
			}
			state = "error"
			before := posts
			if _, err := call(context.Background(), 7); !errors.Is(err, rocketwork.ErrFormat) || posts != before {
				t.Fatal("error status authorized a write")
			}
		})
	}
}
