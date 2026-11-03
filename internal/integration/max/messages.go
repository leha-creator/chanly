package max

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// Bot — сведения о боте (GET /me).
type Bot struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// GetMe проверяет токен и возвращает сведения о боте.
func (c *Client) GetMe(ctx context.Context) (Bot, error) {
	resp, err := c.do(ctx, request{operation: "GetMe", method: http.MethodGet, path: "/me"})
	if err != nil {
		return Bot{}, err
	}
	var b Bot
	if err := json.Unmarshal(resp.Body, &b); err != nil {
		return Bot{}, job.Permanent(fmt.Errorf("max GetMe: decode response: %w", err))
	}
	c.log.DebugContext(ctx, "max bot checked", logx.KeyOp, "MAX.GetMe", "bot_user_id", b.UserID)
	return b, nil
}

// SendChatMessage отправляет текст в чат (например, чат дежурных, R-5).
// Возвращает идентификатор сообщения.
func (c *Client) SendChatMessage(ctx context.Context, chatID int64, text string) (string, error) {
	return c.sendMessage(ctx, "SendChatMessage", "chat_id", chatID, c.chatBucket("chat", chatID), text, nil)
}

// userMessageRejections — отказы MAX на личное сообщение, ожидаемые без начатого диалога (Q-38):
// без повторов и без оповещения администратору.
// ПРЕДПОЛОЖЕНИЕ: бот, которому пользователь не писал, получает 403 (chat.denied) или 404
// (user.not.found) — E-1 плана уведомлений.
var userMessageRejections = []int{http.StatusForbidden, http.StatusNotFound}

// SendUserMessage отправляет текст пользователю MAX (блогеру по
// channel.added_by_max_user_id, A-41). Доставка не гарантирована (Q-38).
func (c *Client) SendUserMessage(ctx context.Context, userID int64, text string) (string, error) {
	return c.sendMessage(ctx, "SendUserMessage", "user_id", userID, c.chatBucket("user", userID), text, userMessageRejections)
}

type newMessage struct {
	Text   string `json:"text"`
	Format string `json:"format,omitempty"`
}

type sentMessage struct {
	Message struct {
		Body struct {
			MID string `json:"mid"`
		} `json:"body"`
	} `json:"message"`
}

func (c *Client) sendMessage(ctx context.Context, op, param string, id int64, bucket ratelimit.Bucket, text string,
	expected []int) (string, error) {
	if text == "" || utf8.RuneCountInString(text) > MaxTextLength {
		return "", job.Permanent(fmt.Errorf("max %s: text length must be 1..%d characters", op, MaxTextLength))
	}
	idStr := strconv.FormatInt(id, 10)
	resp, err := c.do(ctx, request{
		operation: op, method: http.MethodPost, path: "/messages",
		query:   url.Values{param: {idStr}},
		body:    newMessage{Text: text, Format: "markdown"},
		buckets: []ratelimit.Bucket{bucket},
		object:  param, objectID: idStr,
		expected: expected,
	})
	if err != nil {
		return "", err
	}
	var m sentMessage
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return "", job.Permanent(fmt.Errorf("max %s: decode response: %w", op, err))
	}
	// Текст сообщения в журнал не пишется (07 §5.4).
	c.log.InfoContext(ctx, "max message sent", logx.KeyOp, "MAX."+op, param, id, "message_id", m.Message.Body.MID)
	return m.Message.Body.MID, nil
}

// MaxListCount — предел сообщений за один запрос GET /messages (05 §2.3).
const MaxListCount = 100

// ChannelMessage — сообщение канала из MAX. Текста нет: он не хранится и не пишется в
// журнал (07 §5.4); метрикам нужны время, отправитель и просмотры, контролю — хеш (R-13).
type ChannelMessage struct {
	MID string
	// At — время публикации (timestamp, миллисекунды Unix).
	At time.Time
	// FromBot — сообщение отправил бот (например, рекламный пост платформы).
	FromBot bool
	// SenderID — автор; 0 — пост от имени канала (R-8).
	SenderID int64
	// ContentHash — хеш содержимого (R-6).
	ContentHash string
	// Views — stat.views; nil, если MAX поле не вернул (Q-08).
	Views *int
}

// ListMessages читает до count сообщений канала за интервал [from, to], новые первыми
// (GET /messages?chat_id=). В MAX параметр from — верхняя граница, to — нижняя,
// обе в миллисекундах Unix. Бот должен быть администратором канала, иначе 403 — Permanent.
// Приоритет ограничителя — метрики (05 §2.8), если в context не задан другой.
func (c *Client) ListMessages(ctx context.Context, chatID int64, from, to time.Time, count int) ([]ChannelMessage, error) {
	list, err := c.listMessages(ctx, chatID, from, to, count)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelMessage, 0, len(list))
	for _, m := range list {
		out = append(out, m.channelMessage())
	}
	return out, nil
}

// listMessages — GET /messages?chat_id= с текстом сообщений для внутреннего поиска.
// Нулевое to — до текущего момента.
func (c *Client) listMessages(ctx context.Context, chatID int64, from, to time.Time, count int) ([]wireMessage, error) {
	if count < 1 || count > MaxListCount {
		return nil, job.Permanent(fmt.Errorf("max ListMessages: count must be 1..%d", MaxListCount))
	}
	if _, ok := ctx.Value(priorityKey{}).(ratelimit.Priority); !ok {
		ctx = WithPriority(ctx, ratelimit.PriorityMetrics)
	}
	id := strconv.FormatInt(chatID, 10)
	q := url.Values{
		"chat_id": {id},
		"to":      {strconv.FormatInt(from.UnixMilli(), 10)},
		"count":   {strconv.Itoa(count)},
	}
	if !to.IsZero() {
		q.Set("from", strconv.FormatInt(to.UnixMilli(), 10))
	}
	resp, err := c.do(ctx, request{
		operation: "ListMessages", method: http.MethodGet, path: "/messages", query: q,
		object: "chat_id", objectID: id, redact: contentRedact,
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return nil, job.Permanent(fmt.Errorf("max ListMessages: decode response: %w", err))
	}
	c.log.DebugContext(ctx, "max messages read", logx.KeyOp, "MAX.ListMessages", "chat_id", chatID,
		"count", len(body.Messages), "from", from, "to", to)
	return body.Messages, nil
}
