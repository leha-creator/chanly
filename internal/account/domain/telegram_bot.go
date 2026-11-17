package domain

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

type TelegramBotConnection struct {
	ID, UserID           uuid.UUID
	TokenHash, Status    string
	CreatedAt, ExpiresAt time.Time
}

func HashTelegramBotToken(pepper []byte, token string) string {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("notification-telegram-bot:" + token))
	return hex.EncodeToString(mac.Sum(nil))
}

type TelegramBotRepository interface {
	Recipient(context.Context, uuid.UUID) (int64, error)
	CountConnections(context.Context, uuid.UUID, time.Time) (int, error)
	ExpireConnections(context.Context, uuid.UUID) error
	InsertConnection(context.Context, *TelegramBotConnection) error
	Connection(context.Context, uuid.UUID, uuid.UUID, bool) (*TelegramBotConnection, error)
	ConnectionByHash(context.Context, string) (*TelegramBotConnection, error)
	Connect(context.Context, *TelegramBotConnection, int64, time.Time) error
	Disconnect(context.Context, uuid.UUID) error
}
