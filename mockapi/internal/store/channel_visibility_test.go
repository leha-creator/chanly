package store

import "testing"

func TestPrivateChannelApprovalRequiresSameRights(t *testing.T) {
	category := NewID()
	c := &Channel{IsPublic: false, BotIsAdmin: true, BotPermissions: []string{"write", "delete"},
		CategoryID: &category, Prices: map[string]*Price{"f_1_24": {IsEnabled: true}}}
	if missing := ApproveMissing(c); len(missing) != 0 {
		t.Fatalf("private approval blocked: %v", missing)
	}
	c.BotPermissions = []string{"write"}
	if missing := ApproveMissing(c); len(missing) != 1 || missing[0] != "rights" {
		t.Fatalf("private channel bypassed bot rights: %v", missing)
	}
}
