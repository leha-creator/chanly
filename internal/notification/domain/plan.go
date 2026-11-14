package domain

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Audience — адресаты уведомления: пользователи стороны (R-1) и пользователь MAX для бота (R-3).
type Audience struct {
	// UserIDs — члены организации (владелец первым) или администраторы.
	UserIDs []uuid.UUID
	// MaxUserID — channel.added_by_max_user_id канала из данных; nil — бота нет.
	MaxUserID *int64
	// MAXUsers contains explicitly verified personal recipients, keyed by cabinet user.
	MAXUsers map[uuid.UUID]int64
}

// ChannelsFor — каналы доставки стороны (R-2, 07 §6, A-19). Кабинет — всегда.
func ChannelsFor(r Role) []Channel {
	switch r {
	case RoleBlogger, RoleAdvertiser:
		return []Channel{ChannelCabinet, ChannelMaxBot, ChannelEmail}
	default:
		// Администратору почту и чат дежурных шлёт platform/alert (R-9).
		return []Channel{ChannelCabinet}
	}
}

// Plan — строки уведомления для адресатов (R-1…R-4). Кабинет — каждому, сразу sent; почта —
// каждому, pending; бот — каждому пользователю с подтверждённой личной привязкой MAX.
func Plan(n Notice, a Audience, newID func() uuid.UUID, now time.Time) ([]Notification, error) {
	t, err := Lookup(n.Template)
	if err != nil {
		return nil, err
	}
	if !t.Allows(n.Role) {
		return nil, fmt.Errorf("%w: %s for %s", ErrRoleNotAllowed, n.Template, n.Role)
	}
	payload := maps.Clone(n.Data)
	if payload == nil {
		payload = map[string]any{}
	}
	if link := Link(n); link != "" {
		payload[DataLink] = link
	}
	payload["_settings_role"] = string(n.Role)
	var org *uuid.UUID
	if n.OrganizationID != uuid.Nil {
		id := n.OrganizationID
		org = &id
	}
	var objType, objID, key *string
	if n.ObjectType != "" && n.ObjectID != "" {
		objType, objID = ptr(n.ObjectType), ptr(n.ObjectID)
	}
	if n.Key != "" {
		key = ptr(n.Key)
	}
	row := func(user uuid.UUID, ch Channel) Notification {
		st := StatusPending
		var sentAt *time.Time
		if ch == ChannelCabinet {
			st, sentAt = StatusSent, &now
		}
		return Notification{ID: newID(), UserID: user, OrganizationID: org, Channel: ch, Template: n.Template,
			Payload: payload, Status: st, ObjectType: objType, ObjectID: objID, DedupeKey: key, SentAt: sentAt,
			CreatedAt: now, UpdatedAt: now}
	}
	var out []Notification
	for _, ch := range ChannelsFor(n.Role) {
		switch ch {
		case ChannelMaxBot:
			for _, user := range a.UserIDs {
				maxUser := a.MAXUsers[user]
				if maxUser == 0 {
					continue
				}
				r := row(user, ch)
				r.MaxUserID = &maxUser
				r.Payload = maps.Clone(payload)
				r.Payload["_max_bot_binding"] = true
				out = append(out, r)
			}
			// Compatibility for older adapters; production resolves personal bindings.
			if n.Role != RoleBlogger || a.MAXUsers != nil || a.MaxUserID == nil || *a.MaxUserID == 0 || len(a.UserIDs) == 0 {
				continue
			}
			r := row(a.UserIDs[0], ch)
			max := *a.MaxUserID
			r.MaxUserID = &max
			out = append(out, r)
		default:
			for _, u := range a.UserIDs {
				out = append(out, row(u, ch))
			}
		}
	}
	return out, nil
}

// Link — относительный путь в кабинете для перехода из уведомления (R-6).
// ПРЕДПОЛОЖЕНИЕ: адреса экранов кабинета — как в 08 §2 (`adv/order`, `blg/placement`, …) с
// идентификатором последним сегментом; размещение блогера — панель на `blg/placements`.
func Link(n Notice) string {
	id := n.ObjectID
	switch n.Role {
	case RoleAdmin:
		switch n.ObjectType {
		case "placement":
			return "/adm/placements/" + id
		case "channel":
			return "/adm/channels/" + id
		case "order":
			return "/adm/orders/" + id
		}
	case RoleAdvertiser:
		if o := uuidFrom(n.Data[DataOrderID]); o != nil {
			return "/adv/orders/" + o.String()
		}
		switch {
		case n.ObjectType == "order":
			return "/adv/orders/" + id
		case strings.HasPrefix(n.Template, "balance.") || strings.HasPrefix(n.Template, "closing_acts."):
			return "/adv/balance"
		}
	case RoleBlogger:
		switch {
		case n.ObjectType == "placement":
			return "/blg/placements?placement=" + id
		case n.ObjectType == "channel":
			return "/blg/channels/" + id
		case strings.HasPrefix(n.Template, "payout.") || strings.HasPrefix(n.Template, "rocketwork."):
			return "/blg/payouts"
		}
	}
	return ""
}

func ptr[T any](v T) *T { return &v }
