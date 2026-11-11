package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func mockConnectionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func ownMockConnection(ctx context.Context, tx *store.Tx, id uuid.UUID) (*store.ChannelConnection, error) {
	u, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, c := range tx.D.ChannelConnections {
		if c.ID == id && c.OrgID == o.ID && c.UserID == u.ID {
			return c, nil
		}
	}
	return nil, store.NotFound("channel_connection", id)
}

func mockConnectionView(tx *store.Tx, c *store.ChannelConnection) api.ChannelConnection {
	out := api.ChannelConnection{Id: c.ID, Status: api.ChannelConnectionStatus(c.Status), ExpiresAt: c.ExpiresAt,
		ChannelId: c.ChannelID, ChannelName: strPtr(c.ChannelName), Candidates: []api.ChannelConnectionCandidate{}}
	if c.Reason != "" {
		r := api.ChannelConnectionReason(c.Reason)
		out.Reason = &r
	}
	if !tx.Now.Before(c.ExpiresAt) {
		out.Status = api.ChannelConnectionStatusExpired
		return out
	}
	if c.Status == "ready" {
		for _, ch := range tx.D.Channels {
			if slices.Contains(c.Candidates, ch.ID) && (ch.OrgID == nil || *ch.OrgID == c.OrgID) && ch.Status != "disconnected" && ch.Status != "blocked" {
				out.Candidates = append(out.Candidates, api.ChannelConnectionCandidate{Id: ch.ID, Title: ch.Title, Link: ch.Link, IconUrl: ch.IconURL})
			}
		}
	}
	return out
}

func (h *Handlers) CreateChannelConnection(ctx context.Context, req api.CreateChannelConnectionRequestObject) (api.CreateChannelConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.ChannelName)
		if _, _, err := store.ParseChannelReference(name); err != nil {
			return nil, err
		}
	}
	count := 0
	for _, c := range tx.D.ChannelConnections {
		if c.OrgID == o.ID && !c.CreatedAt.Before(tx.Now.Add(-time.Hour)) {
			count++
		}
	}
	if count >= 10 {
		return nil, &store.Error{Code: "rate_limited", Message: "too many channel connections", Details: map[string]any{"scope": "channel_connection"}, Status: 429}
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	c := &store.ChannelConnection{ID: store.NewID(), OrgID: o.ID, UserID: u.ID, TokenHash: mockConnectionHash(token),
		ChannelName: name,
		ExpiresAt:   tx.Now.Add(time.Hour), CreatedAt: tx.Now, UpdatedAt: tx.Now, Status: "pending"}
	tx.D.ChannelConnections = append(tx.D.ChannelConnections, c)
	var botURL *string
	if name != "" {
		mockConnectByName(tx, c)
	} else {
		botURL = strPtr("https://max.ru/" + strings.TrimPrefix(BotName, "@") + "?start=connect_" + token)
	}
	h.logger(ctx).Info("channel connection created", "op", "Channel.Connection.Create", "connection_id", c.ID)
	response := api.CreateChannelConnection201JSONResponse{Id: c.ID, Status: api.CreateChannelConnection201JSONResponseBodyStatus(c.Status),
		ExpiresAt: c.ExpiresAt, Candidates: []api.ChannelConnectionCandidate{}, ChannelName: strPtr(c.ChannelName), ChannelId: c.ChannelID, BotUrl: botURL}
	if c.Reason != "" {
		r := api.CreateChannelConnection201JSONResponseBodyReason(c.Reason)
		response.Reason = &r
	}
	return response, nil
}

func (h *Handlers) GetChannelConnection(ctx context.Context, req api.GetChannelConnectionRequestObject) (api.GetChannelConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	c, err := ownMockConnection(ctx, tx, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetChannelConnection200JSONResponse(mockConnectionView(tx, c)), nil
}

func (h *Handlers) RefreshChannelConnection(ctx context.Context, req api.RefreshChannelConnectionRequestObject) (api.RefreshChannelConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	c, err := ownMockConnection(ctx, tx, req.Id)
	if err != nil {
		return nil, err
	}
	if !tx.Now.Before(c.ExpiresAt) || (c.MaxUserID == 0 && c.ChannelName == "") || c.Status == "connected" {
		return nil, store.Conflict("conflict", "connection cannot be refreshed", nil)
	}
	if tx.Now.Sub(c.UpdatedAt) < 10*time.Second {
		return nil, &store.Error{Code: "rate_limited", Message: "refresh too soon", Details: map[string]any{"scope": "channel_connection_refresh"}, Status: 429}
	}
	c.Status = "ready"
	c.Reason = ""
	c.ChannelID = nil
	c.UpdatedAt = tx.Now
	if c.ChannelName != "" {
		mockConnectByName(tx, c)
	}
	return api.RefreshChannelConnection202Response{}, nil
}

func (h *Handlers) ClaimChannelConnection(ctx context.Context, req api.ClaimChannelConnectionRequestObject) (api.ClaimChannelConnectionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	c, err := ownMockConnection(ctx, tx, req.Id)
	if err != nil {
		return nil, err
	}
	if !tx.Now.Before(c.ExpiresAt) {
		return nil, store.Conflict("conflict", "connection expired", nil)
	}
	if c.Status == "connected" && c.ChannelID != nil && *c.ChannelID == req.Body.ChannelId {
		return api.ClaimChannelConnection202Response{}, nil
	}
	if c.Status != "ready" {
		return nil, store.Conflict("conflict", "connection is not ready", nil)
	}
	if !slices.Contains(c.Candidates, req.Body.ChannelId) {
		return nil, store.NotFound("channel_candidate", req.Body.ChannelId)
	}
	c.ChannelID = &req.Body.ChannelId
	c.UpdatedAt = tx.Now
	for _, ch := range tx.D.Channels {
		if ch.ID == req.Body.ChannelId {
			c.Status = "failed"
			switch {
			case ch.OrgID != nil && *ch.OrgID != c.OrgID:
				c.Reason = "linked_to_other"
			case ch.Status == "disconnected" || ch.Status == "blocked":
				c.Reason = "channel_unavailable"
			case !ch.BotIsAdmin || !slices.Contains(ch.BotPermissions, "write") || !slices.Contains(ch.BotPermissions, "delete"):
				c.Reason = "bot_rights_required"
			default:
				ch.OrgID = &c.OrgID
				if ch.Status == "unclaimed" {
					ch.Status = "pending_rights"
					tx.RecheckChannel(ch)
				}
				c.Status = "connected"
				c.Reason = ""
			}
			return api.ClaimChannelConnection202Response{}, nil
		}
	}
	c.Status = "failed"
	c.Reason = "channel_unavailable"
	return api.ClaimChannelConnection202Response{}, nil
}

// Mock-only: bot_started authenticates the link and provides a canned channel
// in place of worker discovery and live MAX membership queries.
func mockStartConnection(tx *store.Tx, body map[string]interface{}) {
	kind, _ := body["update_type"].(string)
	payload, _ := body["payload"].(string)
	user, _ := body["user"].(map[string]interface{})
	userID, _ := user["user_id"].(float64)
	isBot, _ := user["is_bot"].(bool)
	if kind != "bot_started" || !strings.HasPrefix(payload, "connect_") || userID <= 0 || isBot {
		return
	}
	hash := mockConnectionHash(strings.TrimPrefix(payload, "connect_"))
	for _, c := range tx.D.ChannelConnections {
		if c.TokenHash == hash && c.Status == "pending" && tx.Now.Before(c.ExpiresAt) {
			c.MaxUserID = int64(userID)
			c.Status = "ready"
			c.UpdatedAt = tx.Now
			tx.D.NextMaxChatID--
			ch := &store.Channel{ID: store.NewID(), MaxChatID: tx.D.NextMaxChatID, Title: "Канал для подключения", Status: "unclaimed",
				IsPublic: true, BotIsAdmin: true, BotPermissions: []string{"write", "delete"}, RknStatus: "not_required", Rating: 100,
				Prices: map[string]*store.Price{}, CreatedAt: tx.Now}
			tx.D.Channels = append(tx.D.Channels, ch)
			c.Candidates = []uuid.UUID{ch.ID}
		}
	}
}
