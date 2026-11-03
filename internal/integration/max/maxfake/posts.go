package maxfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
)

// FakeBotUserID — пользователь MAX поддельного бота (GetMe, BotUserID).
const FakeBotUserID int64 = 1

// Post — пост, опубликованный поддельным ботом в канале.
type Post struct {
	ChatID      int64
	ID          string
	Text        string
	ImageTokens []string
	At          time.Time
	Hash        string
	Views       *int
	Removed     bool
}

// SetNow задаёт часы поддельного MAX: время постов и сообщений (по умолчанию — нулевое время).
func (c *Client) SetNow(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *Client) clockNow() time.Time {
	if c.now == nil {
		return time.Time{}
	}
	return c.now().UTC()
}

func fakeHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// BotUserID — пользователь поддельного бота.
func (c *Client) BotUserID(context.Context) (int64, error) { return FakeBotUserID, nil }

// UploadImage читает файл и возвращает новый токен.
func (c *Client) UploadImage(_ context.Context, _ int64, r io.Reader, size int64, _ string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return "", c.failErr
	}
	if c.uploadErr != nil {
		return "", c.uploadErr
	}
	if int64(len(data)) != size {
		return "", fmt.Errorf("maxfake: upload size %d, want %d", len(data), size)
	}
	c.seq++
	c.uploads++
	return fmt.Sprintf("token.fake.%d", c.seq), nil
}

// SendChannelPost публикует пост: он появляется в ListMessages канала от имени бота.
// FailSendAfterCreate — пост создаётся, но вызов возвращает ошибку (неизвестный исход).
func (c *Client) SendChannelPost(_ context.Context, chatID int64, text string, tokens []string, _ max.Recovery) (max.PostedMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return max.PostedMessage{}, c.failErr
	}
	if c.sendErr != nil {
		return max.PostedMessage{}, c.sendErr
	}
	c.seq++
	p := Post{ChatID: chatID, ID: fmt.Sprintf("mid.post.%d", c.seq), Text: text, ImageTokens: slices.Clone(tokens),
		At: c.clockNow()}
	p.Hash = fakeHash(append([]string{text}, tokens...)...)
	if c.posts == nil {
		c.posts = map[string]*Post{}
	}
	c.posts[p.ID] = &p
	c.postOrder = append(c.postOrder, p.ID)
	if c.sendAfterCreateErr != nil {
		err := c.sendAfterCreateErr
		c.sendAfterCreateErr = nil
		return max.PostedMessage{}, err
	}
	return max.PostedMessage{ID: p.ID, URL: "https://max.ru/c/" + p.ID, At: p.At, ContentHash: p.Hash}, nil
}

// GetMessage — пост поддельного бота; удалённый или чужой — max.ErrMessageNotFound.
func (c *Client) GetMessage(_ context.Context, messageID string) (max.ChannelMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return max.ChannelMessage{}, c.failErr
	}
	p, ok := c.posts[messageID]
	if !ok || p.Removed {
		return max.ChannelMessage{}, max.ErrMessageNotFound
	}
	return max.ChannelMessage{MID: p.ID, At: p.At, FromBot: true, SenderID: FakeBotUserID, ContentHash: p.Hash, Views: p.Views}, nil
}

// FindPost ищет живой пост бота в канале за [from, to], текст которого содержит needle.
func (c *Client) FindPost(_ context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*max.ChannelMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return nil, c.failErr
	}
	for _, id := range slices.Backward(c.postOrder) {
		p := c.posts[id]
		if p.Removed || p.ChatID != chatID || senderID != FakeBotUserID || !strings.Contains(p.Text, needle) ||
			p.At.Before(from) || (!to.IsZero() && p.At.After(to)) {
			continue
		}
		return &max.ChannelMessage{MID: p.ID, At: p.At, FromBot: true, SenderID: FakeBotUserID, ContentHash: p.Hash, Views: p.Views}, nil
	}
	return nil, nil
}

// postMessages — живые посты бота канала как сообщения списка.
func (c *Client) postMessages(chatID int64) []max.ChannelMessage {
	var out []max.ChannelMessage
	for _, id := range c.postOrder {
		p := c.posts[id]
		if p.ChatID == chatID && !p.Removed {
			out = append(out, max.ChannelMessage{MID: p.ID, At: p.At, FromBot: true, SenderID: FakeBotUserID,
				ContentHash: p.Hash, Views: p.Views})
		}
	}
	return out
}

// Posts — копия опубликованных постов по порядку.
func (c *Client) Posts() []Post {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Post, 0, len(c.postOrder))
	for _, id := range c.postOrder {
		out = append(out, *c.posts[id])
	}
	return out
}

// Uploads — сколько изображений загружено.
func (c *Client) Uploads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uploads
}

// SetViews задаёт просмотры поста.
func (c *Client) SetViews(messageID string, views int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.posts[messageID]; ok {
		p.Views = &views
	}
}

// RemoveExternally — пост удалил не бот (S-26): GetMessage его больше не находит.
func (c *Client) RemoveExternally(messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.posts[messageID]; ok {
		p.Removed = true
	}
}

// EditExternally — пост изменили в канале (S-26): хеш содержимого меняется.
func (c *Client) EditExternally(messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.posts[messageID]; ok {
		p.Hash = fakeHash(p.Hash, "edited")
	}
}

// PostAsHuman — человек опубликовал сообщение в канале в момент at (S-27).
func (c *Client) PostAsHuman(chatID int64, senderID int64, at time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("mid.human.%d", c.seq)
	if c.messages == nil {
		c.messages = map[int64][]max.ChannelMessage{}
	}
	c.messages[chatID] = append(c.messages[chatID], max.ChannelMessage{MID: id, At: at.UTC(), SenderID: senderID})
	return id
}

// FailSendWith заставляет SendChannelPost возвращать err без создания поста; nil — снова работать.
func (c *Client) FailSendWith(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErr = err
}

// FailSendAfterCreate — следующий SendChannelPost создаст пост, но вернёт err (неизвестный исход).
func (c *Client) FailSendAfterCreate(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendAfterCreateErr = err
}

// FailUploadsWith заставляет UploadImage возвращать err; nil — снова работать.
func (c *Client) FailUploadsWith(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.uploadErr = err
}

// FailDeletesWith заставляет DeleteMessage возвращать err; nil — снова работать.
func (c *Client) FailDeletesWith(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteErr = err
}
