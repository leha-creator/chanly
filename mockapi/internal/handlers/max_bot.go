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

func (h *Handlers) GetMaxBot(ctx context.Context, _ api.GetMaxBotRequestObject) (api.GetMaxBotResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetMaxBot200JSONResponse{Connected: tx.D.MaxBotBindings[u.ID] > 0}, nil
}
func (h *Handlers) DisconnectMaxBot(ctx context.Context, _ api.DisconnectMaxBotRequestObject) (api.DisconnectMaxBotResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	tx.D.MaxBotBindings[u.ID] = 0
	for _, c := range tx.D.MaxBotConnections {
		if c.UserID == u.ID {
			c.Status = "expired"
		}
	}
	return api.DisconnectMaxBot204Response{}, nil
}
func (h *Handlers) CreateMaxBotConnection(ctx context.Context, _ api.CreateMaxBotConnectionRequestObject) (api.CreateMaxBotConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, c := range tx.D.MaxBotConnections {
		if c.UserID == u.ID && c.CreatedAt.After(tx.Now.Add(-time.Hour)) {
			count++
		}
	}
	if count >= 5 {
		return nil, store.SettingsRateLimited("max_bot_connection", time.Hour)
	}
	for _, c := range tx.D.MaxBotConnections {
		if c.UserID == u.ID && c.Status == "pending" {
			c.Status = "expired"
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	c := &store.MaxBotConnection{ID: store.NewID(), UserID: u.ID, TokenHash: mockConnectionHash(token), Status: "pending", ExpiresAt: tx.Now.Add(10 * time.Minute), CreatedAt: tx.Now}
	tx.D.MaxBotConnections = append(tx.D.MaxBotConnections, c)
	return api.CreateMaxBotConnection201JSONResponse{Id: c.ID, Status: "pending", ExpiresAt: c.ExpiresAt, BotUrl: "https://max.ru/chanly_bot?start=notify_" + token}, nil
}
func (h *Handlers) GetMaxBotConnection(ctx context.Context, req api.GetMaxBotConnectionRequestObject) (api.GetMaxBotConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, c := range tx.D.MaxBotConnections {
		if c.ID == req.Id && c.UserID == u.ID {
			status := c.Status
			if status == "pending" && !tx.Now.Before(c.ExpiresAt) {
				status = "expired"
			}
			return api.GetMaxBotConnection200JSONResponse{Id: c.ID, Status: api.MaxBotConnectionStatus(status), ExpiresAt: c.ExpiresAt}, nil
		}
	}
	return nil, store.NotFound("max_bot_connection", req.Id)
}

// Mock-only simulation of a verified MAX bot_started webhook.
func mockStartMaxBotConnection(tx *store.Tx, body map[string]interface{}) {
	kind, _ := body["update_type"].(string)
	payload, _ := body["payload"].(string)
	user, _ := body["user"].(map[string]interface{})
	userID, _ := user["user_id"].(float64)
	isBot, _ := user["is_bot"].(bool)
	if kind != "bot_started" || !strings.HasPrefix(payload, "notify_") || userID <= 0 || userID >= float64(math.MaxInt64) || math.Trunc(userID) != userID || isBot {
		return
	}
	hash := mockConnectionHash(strings.TrimPrefix(payload, "notify_"))
	for _, c := range tx.D.MaxBotConnections {
		if c.TokenHash == hash && c.Status == "pending" && tx.Now.Before(c.ExpiresAt) {
			u := tx.D.Users[c.UserID]
			if u == nil || u.Blocked {
				return
			}
			tx.D.MaxBotBindings[c.UserID] = int64(userID)
			c.Status = "connected"
			return
		}
	}
}
