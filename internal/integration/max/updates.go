package max

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Виды событий Webhook (05 §2.2, объект Update).
const (
	UpdateBotStarted                 = "bot_started"
	UpdateBotAdded                   = "bot_added"
	UpdateBotRemoved                 = "bot_removed"
	UpdateBotAdminPermissionsChanged = "bot_admin_permissions_changed"
	UpdateMessageCreated             = "message_created"
	UpdateMessageEdited              = "message_edited"
	UpdateMessageRemoved             = "message_removed"
	UpdateChatTitleChanged           = "chat_title_changed"
)

// ChannelUpdateTypes — события, на которые подписывается бот (R-5): каналы, коды
// подключения и контроль публикаций (R-17 этапа «Автопостинг и контроль»).
var ChannelUpdateTypes = []string{
	UpdateBotStarted,
	UpdateBotAdded, UpdateBotRemoved, UpdateBotAdminPermissionsChanged,
	UpdateMessageCreated, UpdateMessageEdited, UpdateMessageRemoved, UpdateChatTitleChanged,
}

// User — пользователь MAX в событии.
type User struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

// Message — сообщение в событии message_created.
type Message struct {
	Sender    *User `json:"sender"`
	Recipient struct {
		ChatID   int64  `json:"chat_id"`
		ChatType string `json:"chat_type"`
		UserID   int64  `json:"user_id"`
	} `json:"recipient"`
	Timestamp int64 `json:"timestamp"`
	Body      struct {
		MID         string           `json:"mid"`
		Text        string           `json:"text"`
		Markup      json.RawMessage  `json:"markup"`
		Attachments []wireAttachment `json:"attachments"`
	} `json:"body"`
}

// Update — событие Webhook MAX. Поля, которых нет у вида события, пустые.
type Update struct {
	// Internal field, computed by our authenticated webhook before persistence.
	ConnectionTokenHash string `json:"connection_token_hash"`
	Payload             string `json:"payload"`
	Type                string `json:"update_type"`
	Timestamp           int64  `json:"timestamp"`
	// ChatID — чат события; для message_created берётся из получателя сообщения.
	ChatID    int64    `json:"chat_id"`
	User      *User    `json:"user"`
	IsChannel bool     `json:"is_channel"`
	Title     string   `json:"title"`
	Message   *Message `json:"message"`
	// RemovedMessageID и RemovedBy — событие message_removed: удалённое сообщение и кто удалил.
	RemovedMessageID string `json:"message_id"`
	RemovedBy        int64  `json:"user_id"`
}

// ErrNoUpdateType — тело не похоже на событие MAX.
var ErrNoUpdateType = errors.New("max update: update_type is missing")

// ParseUpdate разбирает тело события. Неизвестный вид — не ошибка: его сохраняют
// как ignored (05 §2.2).
func ParseUpdate(body []byte) (Update, error) {
	var u Update
	if err := json.Unmarshal(body, &u); err != nil {
		return Update{}, fmt.Errorf("max update: %w", err)
	}
	if u.Type == "" {
		return Update{}, ErrNoUpdateType
	}
	if u.ChatID == 0 && u.Message != nil {
		u.ChatID = u.Message.Recipient.ChatID
	}
	return u, nil
}

// InDialog — сообщение написано боту в личном диалоге.
func (u Update) InDialog() bool {
	return u.Message != nil && u.Message.Recipient.ChatType == ChatTypeDialog
}

// InChannel — сообщение опубликовано в канале.
func (u Update) InChannel() bool {
	return u.Message != nil && u.Message.Recipient.ChatType == ChatTypeChannel
}

// SenderID — автор сообщения или 0, если MAX его не передал (пост от имени канала).
func (u Update) SenderID() int64 {
	if u.Message == nil || u.Message.Sender == nil {
		return 0
	}
	return u.Message.Sender.UserID
}

// UserID — пользователь события (кто добавил или удалил бота) или 0.
func (u Update) UserID() int64 {
	if u.User == nil {
		return 0
	}
	return u.User.UserID
}

// ChatIDString — идентификатор чата для журнала и ключей.
func (u Update) ChatIDString() string { return strconv.FormatInt(u.ChatID, 10) }

// MessageID — сообщение события: удалённое (message_removed) или из тела события.
func (u Update) MessageID() string {
	if u.RemovedMessageID != "" {
		return u.RemovedMessageID
	}
	if u.Message != nil {
		return u.Message.Body.MID
	}
	return ""
}

// MessageAt — время сообщения по MAX (S-27: время нарушения), иначе время события.
func (u Update) MessageAt() time.Time {
	if u.Message != nil && u.Message.Timestamp > 0 {
		return time.UnixMilli(u.Message.Timestamp).UTC()
	}
	return time.UnixMilli(u.Timestamp).UTC()
}

// ContentHash — хеш сообщения события (message_edited) той же функцией, что у публикации (R-6).
func (u Update) ContentHash() string {
	if u.Message == nil {
		return ""
	}
	return ContentHash(u.Message.Body.Text, u.Message.Body.Markup, u.Message.Body.Attachments)
}
