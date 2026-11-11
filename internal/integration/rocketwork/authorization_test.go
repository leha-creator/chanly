package rocketwork

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestReadOnlyBoundaryRejectsEveryWrite(t *testing.T) {
	c := New(nil, nil, Options{Token: "synthetic-never-sent"}, logx.Discard())
	for _, path := range []string{"/api/tasks", "/api/tasks/7/pay", "/api/tasks/7/complete", "/api/tasks/7/sign_act", "/api/tasks/7/cancel", "/api/executors", "/api/executors/invite", "/api/executors/rebind", "/api/subscriptions", "/unknown"} {
		for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
			if err := c.write(context.Background(), "test", method, path, map[string]any{"type": "Withdrawal"}, nil); !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("%s %s: %v", method, path, err)
			}
		}
	}
}
func TestEmulatorCapabilityCannotReachProduction(t *testing.T) {
	for _, origin := range []string{DefaultBaseURL, "http://localhost:1234", "http://127.0.0.1.evil.test", "http://127.0.0.1@api.rocketwork.ru"} {
		if _, err := LocalEmulatorAuthorization(origin); err == nil {
			t.Fatalf("authorized %s", origin)
		}
	}
	auth, err := LocalEmulatorAuthorization("http://127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	c := New(nil, nil, Options{BaseURL: DefaultBaseURL, Authorizer: auth}, logx.Discard())
	if err := c.write(context.Background(), "test", "POST", "/api/tasks", nil, nil); !errors.Is(err, ErrPolicyDenied) {
		t.Fatal(err)
	}
}
