package domain

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

type MAXBotConnection struct {
	ID, UserID           uuid.UUID
	TokenHash, Status    string
	CreatedAt, ExpiresAt time.Time
}

func HashMAXBotToken(pepper []byte, token string) string {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("notification-max-bot:" + token))
	return hex.EncodeToString(mac.Sum(nil))
}

type MAXBotRepository interface {
	Recipient(context.Context, uuid.UUID) (int64, error)
	CountConnections(context.Context, uuid.UUID, time.Time) (int, error)
	ExpireConnections(context.Context, uuid.UUID) error
	InsertConnection(context.Context, *MAXBotConnection) error
	Connection(context.Context, uuid.UUID, uuid.UUID, bool) (*MAXBotConnection, error)
	ConnectionByHash(context.Context, string) (*MAXBotConnection, error)
	Connect(context.Context, *MAXBotConnection, int64, time.Time) error
	Disconnect(context.Context, uuid.UUID) error
}
