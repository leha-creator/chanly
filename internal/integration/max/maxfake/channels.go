package maxfake

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
)

// SetChat задаёт сведения о чате, которые вернёт GetChat.
func (c *Client) SetChat(chat max.Chat) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chats == nil {
		c.chats = map[int64]max.Chat{}
	}
	c.chats[chat.ChatID] = chat
}

// SetMembership задаёт права бота в чате, которые вернёт GetMembership.
func (c *Client) SetMembership(chatID int64, m max.Membership) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.members == nil {
		c.members = map[int64]max.Membership{}
	}
	c.members[chatID] = m
}

// GetChat возвращает чат, заданный SetChat; неизвестный чат — отказ chat.not.found.
func (c *Client) GetChat(_ context.Context, chatID int64) (max.Chat, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return max.Chat{}, c.failErr
	}
	chat, ok := c.chats[chatID]
	if !ok {
		return max.Chat{}, fmt.Errorf("maxfake: chat %d not found", chatID)
	}
	return chat, nil
}

// GetMembership возвращает права, заданные SetMembership; по умолчанию бот не администратор.
func (c *Client) GetMembership(_ context.Context, chatID int64) (max.Membership, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return max.Membership{}, c.failErr
	}
	m := c.members[chatID]
	m.Permissions = slices.Clone(m.Permissions)
	if m.Permissions == nil {
		m.Permissions = []string{}
	}
	return m, nil
}

// DeleteMessage запоминает удалённое сообщение.
func (c *Client) DeleteMessage(_ context.Context, _ int64, messageID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return c.failErr
	}
	if c.deleteErr != nil {
		return c.deleteErr
	}
	if p, ok := c.posts[messageID]; ok {
		p.Removed = true
	}
	c.deleted = append(c.deleted, messageID)
	return nil
}

// LeaveChat запоминает чат, который покинул бот.
func (c *Client) LeaveChat(_ context.Context, chatID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return c.failErr
	}
	c.left = append(c.left, chatID)
	return nil
}

// ListSubscriptions возвращает подписки, созданные Subscribe.
func (c *Client) ListSubscriptions(context.Context) ([]max.Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return nil, c.failErr
	}
	return slices.Clone(c.subs), nil
}

// Subscribe запоминает подписку.
func (c *Client) Subscribe(_ context.Context, url string, updateTypes []string, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return c.failErr
	}
	// Подписка на тот же адрес заменяет прежнюю, как в MAX.
	c.subs = slices.DeleteFunc(c.subs, func(s max.Subscription) bool { return s.URL == url })
	c.subs = append(c.subs, max.Subscription{URL: url, UpdateTypes: slices.Clone(updateTypes)})
	return nil
}

// Deleted — идентификаторы удалённых сообщений.
func (c *Client) Deleted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.deleted)
}

// Left — чаты, которые покинул бот.
func (c *Client) Left() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.left)
}

// SetMessages задаёт сообщения канала, которые вернёт ListMessages.
func (c *Client) SetMessages(chatID int64, msgs []max.ChannelMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.messages == nil {
		c.messages = map[int64][]max.ChannelMessage{}
	}
	c.messages[chatID] = slices.Clone(msgs)
}

// FailMessagesWith заставляет только ListMessages возвращать err; nil — снова работать.
func (c *Client) FailMessagesWith(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messagesErr = err
}

// ListMessages возвращает до count сообщений из SetMessages за [from, to], новые первыми.
func (c *Client) ListMessages(_ context.Context, chatID int64, from, to time.Time, count int) ([]max.ChannelMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return nil, c.failErr
	}
	if c.messagesErr != nil {
		return nil, c.messagesErr
	}
	out := []max.ChannelMessage{}
	for _, m := range append(slices.Clone(c.messages[chatID]), c.postMessages(chatID)...) {
		if !m.At.Before(from) && !m.At.After(to) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b max.ChannelMessage) int { return b.At.Compare(a.At) })
	if len(out) > count {
		out = out[:count]
	}
	return out, nil
}
