package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestSubscriptionContractAndOwnedDeletion(t *testing.T) {
	validate := contractValidator(t)
	spec := rocketwork.SubscriptionRequest{URI: "https://callback.example.test/rocketwork", Method: "post", Authorization: rocketwork.SubscriptionAuthorization{Type: "Bearer", Value: "synthetic-subscription-secret"}, RetryLimit: 15, Streams: []string{"tasks", "executors"}}
	row := rocketwork.Subscription{ID: 41, URI: spec.URI, Method: "post", Authorization: spec.Authorization, RetryLimit: 15, Version: "v2", Streams: spec.Streams}
	writes := 0
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/postback_subscriptions" && r.URL.Path != "/api/postback_subscriptions/41" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if err := validate("/components/schemas/PostbackSubscriptions", []rocketwork.Subscription{row}); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode([]rocketwork.Subscription{row})
		case http.MethodPost:
			writes++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, exists := body["version"]; exists {
				t.Error("undocumented version in request")
			}
			if err := validate("/components/schemas/PostbackSubscriptionCreateParams", body); err != nil {
				t.Error(err)
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(row)
		case http.MethodDelete:
			writes++
			_ = json.NewEncoder(w).Encode(row)
		default:
			t.Error("unexpected method")
		}
	})
	got, err := c.CreateSubscription(context.Background(), spec)
	if err != nil || got.ID != 41 {
		t.Fatal(err)
	}
	foreign := spec
	foreign.Authorization.Value = "other-owner"
	if err := c.DeleteSubscription(context.Background(), 41, foreign); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("foreign subscription deletion accepted")
	}
	if writes != 1 {
		t.Fatal("foreign DELETE sent")
	}
	if err := c.DeleteSubscription(context.Background(), 41, spec); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatal("missing owned DELETE")
	}
}

func TestPostbackHistoryUsesClosedIntervalAndRequiresStatus(t *testing.T) {
	validate := contractValidator(t)
	from := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	through := from.Add(time.Hour)
	status := true
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/postbacks" || r.URL.Query().Get("page[page]") != "2" || r.URL.Query().Get("filter[happened_at_gte]") != from.Format(time.RFC3339) || r.URL.Query().Get("filter[happened_at_lte]") != through.Format(time.RFC3339) {
			t.Error("wrong history query")
		}
		event := map[string]any{"id": 22, "event": "executors.activated", "happened_at": from.Format(time.RFC3339), "data": map[string]any{"inn": "000000000000"}}
		if status {
			event["status"] = "created"
		}
		response := map[string]any{"postbacks": []any{event}, "meta": map[string]any{"current": 2, "last": 2, "total": 101}}
		if status {
			if err := validate("/components/schemas/Postbacks", response); err != nil {
				t.Error(err)
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	page, err := c.PostbackHistory(context.Background(), from, through, 2)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "executor_refresh" || page.Events[0].ObjectID != "" {
		t.Fatalf("history: %+v %v", page, err)
	}
	status = false
	if _, err := c.PostbackHistory(context.Background(), from, through, 2); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("history status omission accepted")
	}
}
