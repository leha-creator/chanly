// Package domain — правила модуля уведомлений (04 §8, 07 §6): каталог шаблонов, адресаты и
// каналы доставки, состояния записи. Без базы, сети и журнала.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Channel — канал доставки (04 §9 notification.channel).
type Channel string

const (
	ChannelMaxBot  Channel = "max_bot"
	ChannelEmail   Channel = "email"
	ChannelCabinet Channel = "cabinet"
)

// Status — состояние уведомления (04 §8).
type Status string

const (
	StatusPending    Status = "pending"
	StatusSent       Status = "sent"
	StatusFailed     Status = "failed"
	StatusSuppressed Status = "suppressed"
	// StatusRead — только у кабинета (R-4).
	StatusRead Status = "read"
)

// Role — сторона, которой адресовано уведомление.
type Role string

const (
	RoleAdvertiser Role = "advertiser"
	RoleBlogger    Role = "blogger"
	// RoleAdmin — администраторы платформы (R-9): только кабинет.
	RoleAdmin Role = "admin"
)

// Ключи данных уведомления, которые читает сам модуль.
const (
	DataOrderID        = "order_id"
	DataOrderNumber    = "order_number"
	DataChannelID      = "channel_id"
	DataChannelTitle   = "channel_title"
	DataLink           = "link"
	DataPublicationURL = "publication_url"
)

// Notice — событие, о котором нужно уведомить сторону (R-1). Организация пуста у
// уведомления администратора. Key — ключ повтора для событий из заданий (R-10).
type Notice struct {
	Template       string
	OrganizationID uuid.UUID
	Role           Role
	ObjectType     string
	ObjectID       string
	Data           map[string]any
	Key            string
}

// Notification — уведомление одному адресату в одном канале (R-4).
type Notification struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	OrganizationID *uuid.UUID
	Channel        Channel
	Template       string
	Payload        map[string]any
	Status         Status
	ObjectType     *string
	ObjectID       *string
	DedupeKey      *string
	// MaxUserID — адресат бота MAX (R-3); только у max_bot.
	MaxUserID *int64
	Attempts  int
	LastError *string
	SentAt    *time.Time
	ReadAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OrderID — заказ из данных уведомления (R-13); nil, если нет.
func (n Notification) OrderID() *uuid.UUID {
	return uuidFrom(n.Payload[DataOrderID])
}

// Link — относительный путь в кабинете (R-6); пусто, если нет.
func (n Notification) Link() string {
	s, _ := n.Payload[DataLink].(string)
	return s
}

// Deliverable — уведомление ждёт отправки заданием.
func (n Notification) Deliverable() bool {
	return n.Status == StatusPending && n.Channel != ChannelCabinet
}

// MarkSent — доставлено.
func (n *Notification) MarkSent(now time.Time) {
	n.Status, n.SentAt, n.UpdatedAt, n.LastError = StatusSent, &now, now, nil
}

// MarkAttempt — попытка не удалась, будет повтор; причина — без персональных данных.
func (n *Notification) MarkAttempt(reason string, now time.Time) {
	n.Attempts++
	n.LastError, n.UpdatedAt = &reason, now
}

// MarkFailed — доставка невозможна или попытки исчерпаны (R-7).
func (n *Notification) MarkFailed(reason string, now time.Time) {
	n.Attempts++
	n.Status, n.LastError, n.UpdatedAt = StatusFailed, &reason, now
}

// MarkRead — прочитано в кабинете; повтор и не кабинетное — ErrNotReadable.
func (n *Notification) MarkRead(now time.Time) error {
	if n.Channel != ChannelCabinet {
		return ErrNotReadable
	}
	if n.Status == StatusRead {
		return nil
	}
	n.Status, n.ReadAt, n.UpdatedAt = StatusRead, &now, now
	return nil
}

func uuidFrom(v any) *uuid.UUID {
	switch x := v.(type) {
	case uuid.UUID:
		if x == uuid.Nil {
			return nil
		}
		return &x
	case string:
		id, err := uuid.Parse(x)
		if err != nil || id == uuid.Nil {
			return nil
		}
		return &id
	}
	return nil
}

// MarkExhausted — попытки задания исчерпаны; последняя попытка уже засчитана MarkAttempt.
func (n *Notification) MarkExhausted(reason string, now time.Time) {
	n.Status, n.LastError, n.UpdatedAt = StatusFailed, &reason, now
}

// SettingsRole preserves the original audience even when cabinets change later.
func (n Notification) SettingsRole() Role {
	if role, ok := n.Payload["_settings_role"].(string); ok && role != "" {
		return Role(role)
	}
	link := n.Link()
	if strings.HasPrefix(link, "/blg/") {
		return RoleBlogger
	}
	if strings.HasPrefix(link, "/adv/") {
		return RoleAdvertiser
	}
	return RoleAdmin
}
func (n Notification) SettingsGroup() string { return GroupFor(n.SettingsRole(), n.Template) }
