package handlers

import (
	"slices"
	"strconv"
	"strings"

	"maxim/mockapi/internal/store"
)

func mockConnectByName(tx *store.Tx, c *store.ChannelConnection) {
	c.Status = "failed"
	c.Reason = "channel_not_found"
	c.Candidates = nil
	kind, value, err := store.ParseChannelReference(c.ChannelName)
	if err != nil {
		return
	}
	matches := []*store.Channel{}
	for _, ch := range tx.D.Channels {
		match := kind == "title" && strings.EqualFold(strings.TrimSpace(ch.Title), value) ||
			kind == "link" && strings.TrimRight(ch.Link, "/") == value ||
			kind == "id" && strconv.FormatInt(ch.MaxChatID, 10) == value
		if match {
			matches = append(matches, ch)
		}
	}
	if len(matches) > 1 {
		c.Reason = "channel_name_ambiguous"
		return
	}
	if len(matches) == 0 {
		return
	}
	ch := matches[0]
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
		c.ChannelID = &ch.ID
	}
}
