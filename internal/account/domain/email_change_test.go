package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestEmailChangePurposeBindingAndOneUse(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	pepper := []byte("test-pepper")
	c := EmailChange{ID: uuid.New(), UserID: uuid.New(), NewEmail: "new@example.test", ExpiresAt: now.Add(time.Minute)}
	c.CurrentHash = c.Hash(pepper, "current", "123456")
	c.NewHash = c.Hash(pepper, "new", "234567")
	for _, modify := range []func(*EmailChange){func(c *EmailChange) { c.UserID = uuid.New() }, func(c *EmailChange) { c.ID = uuid.New() }, func(c *EmailChange) { c.NewEmail = "other@example.test" }} {
		other := c
		modify(&other)
		if err := other.Verify(pepper, "123456", "234567", now); !apperr.Is(err, apperr.CodeValidationFailed) {
			t.Fatal("unbound code")
		}
	}
	if err := c.Verify(pepper, "234567", "123456", now); err == nil {
		t.Fatal("purpose swap accepted")
	}
	if err := c.Verify(pepper, "123456", "234567", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(pepper, "123456", "234567", now); err == nil {
		t.Fatal("code reused")
	}
	c.ConsumedAt = nil
	c.Attempts = 5
	if err := c.Verify(pepper, "123456", "234567", now); !apperr.Is(err, apperr.CodeRateLimited) {
		t.Fatal(err)
	}
	c.Attempts = 0
	if err := c.Verify(pepper, "123456", "234567", c.ExpiresAt); err == nil {
		t.Fatal("expired code accepted")
	}
}
