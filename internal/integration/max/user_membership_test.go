package max_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/max"
)

func TestGetUserMembership(t *testing.T) {
	response := `{"members":[{"user_id":5001,"is_admin":true,"permissions":["write"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/chats/-911/members" || r.URL.Query().Get("user_ids") != "5001" {
			t.Errorf("wrong request %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	for _, tc := range []struct {
		body         string
		admin, owner bool
	}{
		{response, true, false},
		{`{"members":[{"user_id":5001,"is_owner":true}]}`, false, true},
		{`{"members":[]}`, false, false},
		{`{"members":[{"user_id":999,"is_admin":true}]}`, false, false},
	} {
		response = tc.body
		m, err := c.GetUserMembership(context.Background(), -911, 5001)
		if err != nil || m.IsAdmin != tc.admin || m.IsOwner != tc.owner {
			t.Fatalf("membership %+v %v", m, err)
		}
	}
	if u, err := max.ParseUpdate([]byte(`{"update_type":"bot_started","payload":"connect_test","user":{"user_id":5001}}`)); err != nil || u.UserID() != 5001 || u.Payload != "connect_test" {
		t.Fatalf("bot_started %+v %v", u, err)
	}
}
