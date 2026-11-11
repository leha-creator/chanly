package domain_test

import (
	"github.com/leha-creator/chanly/internal/channel/domain"
	"testing"
)

func TestConnectionRights(t *testing.T) {
	for _, tc := range []struct {
		m    domain.Membership
		want bool
	}{
		{domain.Membership{}, false},
		{domain.Membership{IsOwner: true}, true},
		{domain.Membership{IsAdmin: true}, false},
		{domain.Membership{Permissions: []string{"write"}}, false},
		{domain.Membership{IsAdmin: true, Permissions: []string{"write"}}, true},
	} {
		if got := tc.m.CanConnect(); got != tc.want {
			t.Errorf("%+v: %v", tc.m, got)
		}
	}
}

func TestConnectionTokens(t *testing.T) {
	a, b := domain.NewConnectionToken(), domain.NewConnectionToken()
	if len(a) != 43 || a == b {
		t.Fatal("tokens are not distinct 256-bit values")
	}
	pepper := []byte("test pepper")
	if domain.HashConnectionToken(pepper, a) == domain.HashLinkCode(pepper, a) {
		t.Fatal("token purposes not separated")
	}
}
