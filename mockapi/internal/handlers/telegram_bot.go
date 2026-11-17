package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"math"
	"strings"
	"time"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func (h *Handlers) GetTelegramBot(ctx context.Context, _ api.GetTelegramBotRequestObject) (api.GetTelegramBotResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetTelegramBot200JSONResponse{Connected: tx.D.TelegramBotBindings[u.ID] > 0}, nil
}
func (h *Handlers) DisconnectTelegramBot(ctx context.Context, _ api.DisconnectTelegramBotRequestObject) (api.DisconnectTelegramBotResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	tx.D.TelegramBotBindings[u.ID] = 0
	for _, c := range tx.D.TelegramBotConnections {
		if c.UserID == u.ID {
			c.Status = "expired"
		}
	}
	return api.DisconnectTelegramBot204Response{}, nil
}
func (h *Handlers) CreateTelegramBotConnection(ctx context.Context, _ api.CreateTelegramBotConnectionRequestObject) (api.CreateTelegramBotConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, c := range tx.D.TelegramBotConnections {
		if c.UserID == u.ID && c.CreatedAt.After(tx.Now.Add(-time.Hour)) {
			count++
		}
	}
	if count >= 5 {
		return nil, store.SettingsRateLimited("telegram_bot_connection", time.Hour)
	}
	for _, c := range tx.D.TelegramBotConnections {
		if c.UserID == u.ID && c.Status == "pending" {
			c.Status = "expired"
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	c := &store.TelegramBotConnection{ID: store.NewID(), UserID: u.ID, TokenHash: mockConnectionHash(token), Status: "pending", ExpiresAt: tx.Now.Add(10 * time.Minute), CreatedAt: tx.Now}
	tx.D.TelegramBotConnections = append(tx.D.TelegramBotConnections, c)
	return api.CreateTelegramBotConnection201JSONResponse{Id: c.ID, Status: "pending", ExpiresAt: c.ExpiresAt, BotUrl: "https://t.me/LordChanlyBot?start=notify_" + token}, nil
}
func (h *Handlers) GetTelegramBotConnection(ctx context.Context, req api.GetTelegramBotConnectionRequestObject) (api.GetTelegramBotConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, c := range tx.D.TelegramBotConnections {
		if c.ID == req.Id && c.UserID == u.ID {
			status := c.Status
			if status == "pending" && !tx.Now.Before(c.ExpiresAt) {
				status = "expired"
			}
			return api.GetTelegramBotConnection200JSONResponse{Id: c.ID, Status: api.TelegramBotConnectionStatus(status), ExpiresAt: c.ExpiresAt}, nil
		}
	}
	return nil, store.NotFound("telegram_bot_connection", req.Id)
}

// Mock-only simulation of a verified Telegram bot_started webhook.
func mockStartTelegramBotConnection(tx *store.Tx, body map[string]interface{}) {
	kind, _ := body["update_type"].(string)
	payload, _ := body["payload"].(string)
	user, _ := body["user"].(map[string]interface{})
	userID, _ := user["user_id"].(float64)
	isBot, _ := user["is_bot"].(bool)
	if kind != "bot_started" || !strings.HasPrefix(payload, "notify_") || userID <= 0 || userID >= float64(math.MaxInt64) || math.Trunc(userID) != userID || isBot {
		return
	}
	hash := mockConnectionHash(strings.TrimPrefix(payload, "notify_"))
	for _, c := range tx.D.TelegramBotConnections {
		if c.TokenHash == hash && c.Status == "pending" && tx.Now.Before(c.ExpiresAt) {
			u := tx.D.Users[c.UserID]
			if u == nil || u.Blocked {
				return
			}
			tx.D.TelegramBotBindings[c.UserID] = int64(userID)
			c.Status = "connected"
			return
		}
	}
}

// TelegramWebhook simulates an authenticated Telegram update only in mockapi.
func (h *Handlers) TelegramWebhook(ctx context.Context, req api.TelegramWebhookRequestObject) (api.TelegramWebhookResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if req.Body == nil {
		return api.TelegramWebhook200Response{}, nil
	}
	m, _ := (*req.Body)["message"].(map[string]interface{})
	chat, _ := m["chat"].(map[string]interface{})
	from, _ := m["from"].(map[string]interface{})
	kind, _ := chat["type"].(string)
	id, _ := from["id"].(float64)
	chatID, _ := chat["id"].(float64)
	text, _ := m["text"].(string)
	fields := strings.Fields(text)
	if kind != "private" || id != chatID || len(fields) != 2 || (fields[0] != "/start" && fields[0] != "/start@LordChanlyBot") {
		return api.TelegramWebhook200Response{}, nil
	}
	mockStartTelegramBotConnection(tx, map[string]interface{}{"update_type": "bot_started", "payload": fields[1], "user": map[string]interface{}{"user_id": id, "is_bot": from["is_bot"]}})
	return api.TelegramWebhook200Response{}, nil
}
