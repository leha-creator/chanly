package domain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Cursor — позиция в списке кабинета: (created_at, id), новые сверху.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// Repository — хранение уведомлений. Реализация — notification/postgres.
type Repository interface {
	OwnCabinetNotification(context.Context, uuid.UUID, uuid.UUID) (Notification, error)
	// Insert вставляет строки; повтор ключа (R-10) пропускается. Возвращает вставленные.
	Insert(ctx context.Context, ns []Notification) ([]Notification, error)
	// GetForUpdate — уведомление под блокировкой (отправка).
	GetForUpdate(ctx context.Context, id uuid.UUID) (Notification, error)
	// Update сохраняет состояние отправки.
	Update(ctx context.Context, n Notification) error
	// ListCabinet — уведомления кабинета пользователя, новые сверху; unread — без read_at.
	ListCabinet(ctx context.Context, userID uuid.UUID, unread bool, after *Cursor, limit int) ([]Notification, error)
	// CountCabinet — число уведомлений кабинета по тому же фильтру.
	CountCabinet(ctx context.Context, userID uuid.UUID, unread bool) (int, error)
	// MarkRead отмечает своё уведомление кабинета; нет или чужое — ErrNotFound.
	MarkRead(ctx context.Context, userID, id uuid.UUID, now time.Time) error
	// MarkAllRead отмечает все непрочитанные пользователя; возвращает число.
	MarkAllRead(ctx context.Context, userID uuid.UUID, now time.Time) (int64, error)
}

// Recipients — адресаты (R-1). Реализация — переходник над account/postgres.
type Recipients interface {
	// Members — пользователи организации, владелец первым.
	Members(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error)
	// Admins — пользователи с ролью администратора.
	Admins(ctx context.Context) ([]uuid.UUID, error)
	// Email — адрес пользователя в момент отправки; пусто — пользователь заблокирован или удалён.
	Email(ctx context.Context, userID uuid.UUID) (string, error)
}

// ChannelInfo — сведения о канале для текста и адресата бота (R-3, R-6).
type ChannelInfo struct {
	Title string
	// AddedByMaxUserID — пользователь MAX, добавивший бота (A-41); 0 — неизвестен.
	AddedByMaxUserID int64
}

// Channels — каналы MAX. Реализация — переходник над channel/postgres.
type Channels interface {
	// Channel — сведения о канале; нет — ErrNotFound.
	Channel(ctx context.Context, id uuid.UUID) (ChannelInfo, error)
}

// Renderer — тексты уведомления по шаблону (R-5). Реализация — notification/render.
type Renderer interface {
	// Cabinet — заголовок и текст для кабинета.
	Cabinet(n Notification) (title, text string, err error)
	// MAX — личное сообщение бота (Markdown MAX).
	MAX(n Notification) (string, error)
	// Email — тема, текст и HTML письма.
	Email(n Notification) (subject, text, html string, err error)
}

// Messenger — личное сообщение бота MAX. Реализация — переходник над integration/max.
// Отказ без повтора (бот не может писать пользователю, Q-38) помечен job.Permanent.
type Messenger interface {
	SendUserMessage(ctx context.Context, maxUserID int64, text string) error
}

// Mailer — письмо. Реализация — integration/mail. Неверный адрес — job.Permanent.
type Mailer interface {
	SendNotification(ctx context.Context, to, subject, text, html string) error
}

// ErrInvalidCursor — курсор страницы испорчен.
var ErrInvalidCursor = errors.New("notification: invalid cursor")

type cursorJSON struct {
	T time.Time `json:"t"`
	I uuid.UUID `json:"i"`
}

// Encode — курсор для ответа API.
func (c Cursor) Encode() string {
	raw, _ := json.Marshal(cursorJSON{T: c.CreatedAt, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor разбирает курсор; пустая строка — первая страница.
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var c cursorJSON
	if err := json.Unmarshal(raw, &c); err != nil || c.I == uuid.Nil || c.T.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &Cursor{CreatedAt: c.T, ID: c.I}, nil
}
