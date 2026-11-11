package domain

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

var ErrChannelAccessDenied = errors.New("MAX channel access denied")

// Connection is a short-lived onboarding flow, owned by one cabinet user.
type Connection struct {
	ID, OrganizationID, UserID uuid.UUID
	TokenHash                  string
	ChannelName                string
	MaxUserID                  *int64
	TokenUsedAt                *time.Time
	ExpiresAt                  time.Time
	Status, Reason             string
	ScanID                     uuid.UUID
	ChannelID                  *uuid.UUID
	CreatedAt, UpdatedAt       time.Time
}

func NewConnectionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashConnectionToken(pepper []byte, token string) string {
	h := hmac.New(sha256.New, pepper)
	h.Write([]byte("channel_connection:" + token))
	return hex.EncodeToString(h.Sum(nil))
}

// CanConnect requires current ownership or publishing rights, never the actor
// recorded when the bot was added.
func (m Membership) CanConnect() bool {
	return m.IsOwner || (m.IsAdmin && slices.Contains(m.Permissions, PermissionWrite))
}

type ConnectionCandidate struct {
	ID                   uuid.UUID
	Title, Link, IconURL string
}

type Connections interface {
	Create(context.Context, *Connection) error
	Get(context.Context, uuid.UUID, bool) (*Connection, error)
	FindTokenForUpdate(context.Context, string) (*Connection, error)
	Save(context.Context, *Connection) error
	CountSince(context.Context, uuid.UUID, time.Time) (int, error)
	ListDiscovered(context.Context, uuid.UUID, uuid.UUID, int) ([]uuid.UUID, error)
	AddCandidate(context.Context, uuid.UUID, uuid.UUID) error
	ClearCandidates(context.Context, uuid.UUID) error
	Candidates(context.Context, uuid.UUID) ([]ConnectionCandidate, error)
	HasCandidate(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	ResolveReference(context.Context, ChannelReference) ([]uuid.UUID, error)
}

// UserMembershipReader is used only by the worker.
type UserMembershipReader interface {
	GetUserMembership(context.Context, int64, int64) (Membership, error)
}
