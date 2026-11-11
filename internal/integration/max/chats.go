package max

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// Типы чатов MAX (Chat.type).
const (
	ChatTypeChannel = "channel"
	ChatTypeChat    = "chat"
	ChatTypeDialog  = "dialog"
)

// Chat — сведения о чате (GET /chats/{chatId}, 05 §2.3).
type Chat struct {
	ChatID            int64  `json:"chat_id"`
	Type              string `json:"type"`
	Status            string `json:"status"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Link              string `json:"link"`
	IsPublic          bool   `json:"is_public"`
	ParticipantsCount int    `json:"participants_count"`
	MessagesCount     int    `json:"messages_count"`
	OwnerID           int64  `json:"owner_id"`
	Icon              *struct {
		URL string `json:"url"`
	} `json:"icon"`
}

// IconURL — адрес значка или пустая строка.
func (c Chat) IconURL() string {
	if c.Icon == nil {
		return ""
	}
	return c.Icon.URL
}

// Membership — бот как участник чата (GET /chats/{chatId}/members/me).
type Membership struct {
	UserID      int64    `json:"user_id"`
	IsOwner     bool     `json:"is_owner"`
	IsAdmin     bool     `json:"is_admin"`
	Permissions []string `json:"permissions"`
}

// GetUserMembership reads a specific human's current rights. An absent member
// has no rights; transport and API failures remain errors.
func (c *Client) GetUserMembership(ctx context.Context, chatID, userID int64) (Membership, error) {
	id := strconv.FormatInt(chatID, 10)
	resp, err := c.do(ctx, request{
		operation: "GetUserMembership", method: http.MethodGet, path: "/chats/" + url.PathEscape(id) + "/members",
		query: url.Values{"user_ids": {strconv.FormatInt(userID, 10)}}, object: "chat_id", objectID: id,
		expected: []int{http.StatusForbidden, http.StatusNotFound},
	})
	if err != nil {
		return Membership{}, err
	}
	var result struct {
		Members []Membership `json:"members"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return Membership{}, job.Permanent(fmt.Errorf("max GetUserMembership: decode: %w", err))
	}
	for _, m := range result.Members {
		if m.UserID == userID {
			return m, nil
		}
	}
	return Membership{}, nil
}

// GetChat читает сведения о чате.
func (c *Client) GetChat(ctx context.Context, chatID int64) (Chat, error) {
	id := strconv.FormatInt(chatID, 10)
	resp, err := c.do(ctx, request{
		operation: "GetChat", method: http.MethodGet, path: "/chats/" + url.PathEscape(id),
		object: "chat_id", objectID: id,
	})
	if err != nil {
		return Chat{}, err
	}
	var chat Chat
	if err := json.Unmarshal(resp.Body, &chat); err != nil {
		return Chat{}, job.Permanent(fmt.Errorf("max GetChat: decode response: %w", err))
	}
	c.log.DebugContext(ctx, "max chat read", logx.KeyOp, "MAX.GetChat", "chat_id", chatID,
		"type", chat.Type, "is_public", chat.IsPublic, "participants_count", chat.ParticipantsCount)
	return chat, nil
}

// GetMembership читает права бота в чате.
func (c *Client) GetMembership(ctx context.Context, chatID int64) (Membership, error) {
	id := strconv.FormatInt(chatID, 10)
	resp, err := c.do(ctx, request{
		operation: "GetMembership", method: http.MethodGet, path: "/chats/" + url.PathEscape(id) + "/members/me",
		object: "chat_id", objectID: id,
	})
	if err != nil {
		return Membership{}, err
	}
	var m Membership
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return Membership{}, job.Permanent(fmt.Errorf("max GetMembership: decode response: %w", err))
	}
	if m.Permissions == nil {
		m.Permissions = []string{}
	}
	c.log.DebugContext(ctx, "max bot membership read", logx.KeyOp, "MAX.GetMembership", "chat_id", chatID,
		"is_admin", m.IsAdmin, "permissions", m.Permissions)
	return m, nil
}

// LeaveChat — бот покидает чат (DELETE /chats/{chatId}/members/me).
func (c *Client) LeaveChat(ctx context.Context, chatID int64) error {
	id := strconv.FormatInt(chatID, 10)
	if _, err := c.do(ctx, request{
		operation: "LeaveChat", method: http.MethodDelete, path: "/chats/" + url.PathEscape(id) + "/members/me",
		object: "chat_id", objectID: id,
	}); err != nil {
		return err
	}
	c.log.InfoContext(ctx, "max chat left", logx.KeyOp, "MAX.LeaveChat", "chat_id", chatID)
	return nil
}

// DeleteMessage удаляет сообщение (DELETE /messages?message_id=). Ответ 200 с
// success=false — ошибка (05 §1). chatID нужен ограничителю: удаление — запись в канал.
func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID string) error {
	if messageID == "" {
		return job.Permanent(fmt.Errorf("max DeleteMessage: empty message id"))
	}
	if _, err := c.do(ctx, request{
		operation: "DeleteMessage", method: http.MethodDelete, path: "/messages",
		query:   url.Values{"message_id": {messageID}},
		buckets: []ratelimit.Bucket{c.chatBucket("chat", chatID)},
		object:  "message_id", objectID: messageID,
	}); err != nil {
		return err
	}
	c.log.InfoContext(ctx, "max message deleted", logx.KeyOp, "MAX.DeleteMessage", "chat_id", chatID, "message_id", messageID)
	return nil
}
