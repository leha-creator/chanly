package max

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// Публикация поста в канал, чтение и поиск сообщений (05 §2.3, §2.6). Текст сообщений
// читается только внутри адаптера — для хеша содержимого и поиска своей публикации — и не
// возвращается наружу, не пишется в журнал и в outbound_request (07 §5.4, R-13).

// ErrMessageNotFound — сообщения нет в MAX: удалено или не существовало.
var ErrMessageNotFound = errors.New("max: message not found")

// codeAttachmentNotReady — вложение ещё обрабатывается (05 §2.6 шаг 4).
const codeAttachmentNotReady = "attachment.not.ready"

// attachmentNotReadyDelays — повторы отправки при attachment.not.ready (05 §2.6 шаг 4).
var attachmentNotReadyDelays = []time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second,
}

// contentRedact — поля с текстом поста и ссылками загрузки, которые не сохраняются в outbound_request.
var contentRedact = []string{"text", "markup", "url"}

// wireAttachment — вложение сообщения в ответе MAX.
type wireAttachment struct {
	Type    string `json:"type"`
	Payload struct {
		PhotoID int64  `json:"photo_id"`
		URL     string `json:"url"`
	} `json:"payload"`
}

// wireMessage — сообщение MAX (Message): ответ POST /messages, GET /messages/{id}, элемент списка.
type wireMessage struct {
	Sender *struct {
		UserID int64 `json:"user_id"`
		IsBot  bool  `json:"is_bot"`
	} `json:"sender"`
	Recipient struct {
		ChatID int64 `json:"chat_id"`
	} `json:"recipient"`
	Timestamp int64 `json:"timestamp"`
	Body      struct {
		MID         string           `json:"mid"`
		Text        string           `json:"text"`
		Markup      json.RawMessage  `json:"markup"`
		Attachments []wireAttachment `json:"attachments"`
	} `json:"body"`
	Stat *struct {
		Views *int `json:"views"`
	} `json:"stat"`
	URL string `json:"url"`
}

func (m wireMessage) senderID() int64 {
	if m.Sender == nil {
		return 0
	}
	return m.Sender.UserID
}

func (m wireMessage) views() *int {
	if m.Stat == nil {
		return nil
	}
	return m.Stat.Views
}

func (m wireMessage) hash() string {
	return ContentHash(m.Body.Text, m.Body.Markup, m.Body.Attachments)
}

func (m wireMessage) channelMessage() ChannelMessage {
	return ChannelMessage{
		MID: m.Body.MID, At: time.UnixMilli(m.Timestamp).UTC(), FromBot: m.Sender != nil && m.Sender.IsBot,
		SenderID: m.senderID(), ContentHash: m.hash(), Views: m.views(),
	}
}

// ContentHash — хеш сообщения в том виде, в каком его хранит MAX (R-6): нормализованный текст,
// разметка и вложения по порядку. Одна функция для ответа отправки, чтения, списка и события
// message_edited. Q-06: совпадение представлений проверяется на тестовом канале.
func ContentHash(text string, markup json.RawMessage, attachments []wireAttachment) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))))
	h.Write([]byte{0})
	if len(markup) > 0 && string(markup) != "null" {
		var buf bytes.Buffer
		if json.Compact(&buf, markup) == nil {
			h.Write(buf.Bytes())
		}
	}
	h.Write([]byte{0})
	for _, a := range attachments {
		ref := a.Payload.URL
		if a.Payload.PhotoID != 0 {
			ref = strconv.FormatInt(a.Payload.PhotoID, 10)
		}
		fmt.Fprintf(h, "%s:%s;", a.Type, ref)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PostedMessage — пост, созданный в канале.
type PostedMessage struct {
	ID          string
	URL         string
	At          time.Time
	ContentHash string
}

// Recovery — как найти свой пост, если отправка закончилась неизвестным исходом (03 §11, R-5):
// сообщение автора SenderID не раньше Since, текст которого содержит Needle (erid версии).
type Recovery struct {
	SenderID int64
	Since    time.Time
	Needle   string
}

type uploadSlot struct {
	URL string `json:"url"`
}

// UploadImage загружает изображение (POST /uploads?type=image, затем файл по выданному адресу)
// и возвращает токен вложения. Таймаут загрузки — 120 с (05 §1). Адрес и токен в журнал не пишутся.
func (c *Client) UploadImage(ctx context.Context, chatID int64, r io.Reader, size int64, contentType string) (string, error) {
	const op = "UploadImage"
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return "", fmt.Errorf("max %s: read file: %w", op, err)
	}
	if int64(len(data)) != size {
		return "", job.Permanent(fmt.Errorf("max %s: file size %d, want %d", op, len(data), size))
	}
	id := strconv.FormatInt(chatID, 10)
	resp, err := c.do(ctx, request{
		operation: op, method: http.MethodPost, path: "/uploads", query: url.Values{"type": {"image"}},
		object: "chat_id", objectID: id, redact: contentRedact,
	})
	if err != nil {
		return "", err
	}
	var slot uploadSlot
	if err := json.Unmarshal(resp.Body, &slot); err != nil || slot.URL == "" {
		return "", job.Permanent(fmt.Errorf("max %s: no upload url in response", op))
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="data"; filename="image"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	if err := c.limiter.Wait(ctx, c.globalBucket(), priority(ctx)); err != nil {
		if errors.Is(err, ratelimit.ErrBusy) {
			return "", job.Retry(err, busyRetry)
		}
		return "", err
	}
	// Параметры адреса загрузки — подпись доступа: в outbound_request они не сохраняются.
	redact := slices.Clone(contentRedact)
	if u, err := url.Parse(slot.URL); err == nil {
		for k := range u.Query() {
			redact = append(redact, k)
		}
	}
	up, err := c.out.Do(ctx, outbound.Call{
		Operation: op + "File", Method: http.MethodPost, URL: slot.URL,
		Header: http.Header{"Authorization": {c.opts.Token}}, RawBody: body.Bytes(), ContentType: mw.FormDataContentType(),
		Upload: true, ObjectType: "chat_id", ObjectID: id, Redact: redact,
	})
	if err != nil {
		return "", err
	}
	token, err := uploadToken(up.Body)
	if err != nil {
		return "", job.Permanent(fmt.Errorf("max %s: %w", op, err))
	}
	c.log.InfoContext(ctx, "max image uploaded", logx.KeyOp, "MAX."+op, "chat_id", chatID, "size", size)
	return token, nil
}

// uploadToken — токен из ответа загрузки: {"token": …} или {"photos": {"<id>": {"token": …}}}.
func uploadToken(body []byte) (string, error) {
	var v struct {
		Token  string `json:"token"`
		Photos map[string]struct {
			Token string `json:"token"`
		} `json:"photos"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	if v.Token != "" {
		return v.Token, nil
	}
	for _, p := range v.Photos {
		if p.Token != "" {
			return p.Token, nil
		}
	}
	return "", errors.New("no token in upload response")
}

type outgoingAttachment struct {
	Type    string `json:"type"`
	Payload struct {
		Token string `json:"token"`
	} `json:"payload"`
}

type outgoingPost struct {
	Text               string               `json:"text"`
	Format             string               `json:"format"`
	Attachments        []outgoingAttachment `json:"attachments,omitempty"`
	DisableLinkPreview bool                 `json:"disable_link_preview"`
}

// SendChannelPost публикует пост в канал (POST /messages?chat_id=, 05 §2.6): format=html,
// без предпросмотра ссылок, изображения по порядку. attachment.not.ready — повторы внутри
// вызова (1, 2, 4, 8, 15, 30 с), это не неудачная попытка. Таймаут — поиск своего поста по rec
// перед повтором, чтобы не опубликовать дважды (03 §11).
func (c *Client) SendChannelPost(ctx context.Context, chatID int64, text string, imageTokens []string, rec Recovery) (PostedMessage, error) {
	const op = "SendChannelPost"
	if text == "" || utf8.RuneCountInString(text) > MaxTextLength {
		return PostedMessage{}, job.Permanent(fmt.Errorf("max %s: text length must be 1..%d characters", op, MaxTextLength))
	}
	post := outgoingPost{Text: text, Format: "html", DisableLinkPreview: true}
	for _, t := range imageTokens {
		a := outgoingAttachment{Type: "image"}
		a.Payload.Token = t
		post.Attachments = append(post.Attachments, a)
	}
	id := strconv.FormatInt(chatID, 10)
	var recovered *wireMessage
	call := request{
		operation: op, method: http.MethodPost, path: "/messages", query: url.Values{"chat_id": {id}},
		body: post, buckets: []ratelimit.Bucket{c.chatBucket("chat", chatID)},
		object: "chat_id", objectID: id, redact: contentRedact, create: true,
		checkExisting: func(ctx context.Context) (bool, error) {
			m, err := c.findPost(ctx, chatID, rec.Since, time.Time{}, rec.SenderID, rec.Needle)
			if err != nil || m == nil {
				return false, err
			}
			recovered = m
			return true, nil
		},
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.do(ctx, call)
		if e, ok := AsAPIError(err); ok && e.Code == codeAttachmentNotReady && attempt < len(attachmentNotReadyDelays) {
			delay := attachmentNotReadyDelays[attempt]
			c.log.WarnContext(ctx, "max attachment not ready, retrying", logx.KeyOp, "MAX."+op, "chat_id", chatID,
				"attempt", attempt+1, "delay", delay.String())
			if err := c.opts.Sleep(ctx, delay); err != nil {
				return PostedMessage{}, err
			}
			continue
		}
		if err != nil {
			return PostedMessage{}, err
		}
		var m wireMessage
		if resp.Recovered && recovered != nil {
			m = *recovered
			c.log.InfoContext(ctx, "max post found after timeout", logx.KeyOp, "MAX."+op, "chat_id", chatID, "message_id", m.Body.MID)
		} else {
			var body struct {
				Message wireMessage `json:"message"`
			}
			if err := json.Unmarshal(resp.Body, &body); err != nil || body.Message.Body.MID == "" {
				return PostedMessage{}, job.Permanent(fmt.Errorf("max %s: no message in response", op))
			}
			m = body.Message
		}
		out := PostedMessage{ID: m.Body.MID, URL: m.URL, ContentHash: m.hash()}
		if m.Timestamp > 0 {
			out.At = time.UnixMilli(m.Timestamp).UTC()
		}
		// Текст поста в журнал не пишется (07 §5.4).
		c.log.InfoContext(ctx, "max post published", logx.KeyOp, "MAX."+op, "chat_id", chatID, "message_id", out.ID,
			"images", len(imageTokens))
		return out, nil
	}
}

// GetMessage читает сообщение (GET /messages/{id}): автор, время, хеш содержимого, просмотры.
// Нет сообщения — ErrMessageNotFound.
func (c *Client) GetMessage(ctx context.Context, messageID string) (ChannelMessage, error) {
	const op = "GetMessage"
	if messageID == "" {
		return ChannelMessage{}, job.Permanent(errors.New("max GetMessage: empty message id"))
	}
	resp, err := c.do(ctx, request{
		operation: op, method: http.MethodGet, path: "/messages/" + url.PathEscape(messageID),
		object: "message_id", objectID: messageID, redact: contentRedact,
	})
	if notFound(err) {
		c.log.DebugContext(ctx, "max message not found", logx.KeyOp, "MAX."+op, "message_id", messageID)
		return ChannelMessage{}, ErrMessageNotFound
	}
	if err != nil {
		return ChannelMessage{}, err
	}
	var m wireMessage
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return ChannelMessage{}, job.Permanent(fmt.Errorf("max %s: decode response: %w", op, err))
	}
	if m.Body.MID == "" {
		// Ответ с обёрткой {"message": …}.
		var wrapped struct {
			Message wireMessage `json:"message"`
		}
		if json.Unmarshal(resp.Body, &wrapped) == nil {
			m = wrapped.Message
		}
	}
	if m.Body.MID == "" {
		return ChannelMessage{}, ErrMessageNotFound
	}
	cm := m.channelMessage()
	c.log.DebugContext(ctx, "max message read", logx.KeyOp, "MAX."+op, "message_id", messageID, "views", cm.Views)
	return cm, nil
}

// notFound — отказ MAX означает, что сообщения нет: 404 или код *.not.found.
func notFound(err error) bool {
	var rej *outbound.RejectedError
	if !errors.As(err, &rej) {
		return false
	}
	if rej.Status == http.StatusNotFound {
		return true
	}
	e, ok := AsAPIError(err)
	return ok && strings.HasSuffix(e.Code, "not.found")
}

// FindPost ищет сообщение автора senderID за [from, to], текст которого содержит needle
// (R-5). Текст не возвращается. nil — не найдено.
func (c *Client) FindPost(ctx context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*ChannelMessage, error) {
	m, err := c.findPost(ctx, chatID, from, to, senderID, needle)
	if err != nil || m == nil {
		return nil, err
	}
	cm := m.channelMessage()
	return &cm, nil
}

func (c *Client) findPost(ctx context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*wireMessage, error) {
	if needle == "" {
		return nil, nil
	}
	list, err := c.listMessages(ctx, chatID, from, to, MaxListCount)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].senderID() == senderID && strings.Contains(list[i].Body.Text, needle) {
			c.log.DebugContext(ctx, "max post found", logx.KeyOp, "MAX.FindPost", "chat_id", chatID, "message_id", list[i].Body.MID)
			return &list[i], nil
		}
	}
	return nil, nil
}

// BotUserID — пользователь MAX бота платформы (R-8): из Options.BotUserID или GET /me один раз.
func (c *Client) BotUserID(ctx context.Context) (int64, error) {
	c.botMu.Lock()
	defer c.botMu.Unlock()
	if c.botUserID != 0 {
		return c.botUserID, nil
	}
	b, err := c.GetMe(ctx)
	if err != nil {
		return 0, err
	}
	c.botUserID = b.UserID
	return c.botUserID, nil
}
