package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func settingsAPI(s store.Settings, a store.SettingsAccess) api.AccountSettings {
	out := api.AccountSettings{Version: s.Version, HideNotificationPreview: s.HideNotificationPreview,
		QuietHours: api.QuietHours{Enabled: s.QuietHours.Enabled, Start: s.QuietHours.Start, End: s.QuietHours.End, Timezone: api.QuietHoursTimezone(s.QuietHours.Timezone)},
		Delivery:   []api.DeliveryPreference{}, Catalog: []api.DeliveryOption{}}
	for _, p := range s.Delivery {
		out.Delivery = append(out.Delivery, api.DeliveryPreference{Group: p.Group, Event: p.Event, Channel: api.DeliveryChannel(p.Channel), Enabled: p.Enabled})
	}
	for _, o := range s.Catalog(a) {
		v := api.DeliveryOption{Group: o.Group, Event: o.Event, Channel: api.DeliveryChannel(o.Channel), Enabled: o.Enabled, Available: o.Available, Mandatory: o.Mandatory}
		if o.Reason != "" {
			reason := o.Reason
			v.Reason = &reason
		}
		out.Catalog = append(out.Catalog, v)
	}
	return out
}
func (h *Handlers) GetAccountSettings(ctx context.Context, _ api.GetAccountSettingsRequestObject) (api.GetAccountSettingsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetAccountSettings200JSONResponse(settingsAPI(tx.AccountSettings(u.ID), tx.AccountSettingsAccess(u))), nil
}
func (h *Handlers) UpdateAccountSettings(ctx context.Context, r api.UpdateAccountSettingsRequestObject) (api.UpdateAccountSettingsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	b := r.Body
	p := store.SettingsPatch{Version: b.Version, HideNotificationPreview: b.HideNotificationPreview}
	if q := b.QuietHours; q != nil {
		p.QuietHours = &store.QuietHours{Enabled: q.Enabled, Start: q.Start, End: q.End, Timezone: string(q.Timezone)}
	}
	if b.Delivery != nil {
		for _, v := range *b.Delivery {
			p.Delivery = append(p.Delivery, store.Preference{Group: v.Group, Event: v.Event, Channel: string(v.Channel), Enabled: v.Enabled})
		}
	}
	a := tx.AccountSettingsAccess(u)
	s, err := tx.AccountSettings(u.ID).Apply(p, a)
	if err != nil {
		return nil, err
	}
	tx.D.Settings[u.ID] = s
	return api.UpdateAccountSettings200JSONResponse(settingsAPI(s, a)), nil
}
func (h *Handlers) ListAccountSessions(ctx context.Context, _ api.ListAccountSessionsRequestObject) (api.ListAccountSessionsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.ListAccountSessions200JSONResponse{Items: tx.OwnSessions(u.ID, tokenFrom(ctx))}, nil
}
func (h *Handlers) RevokeAccountSession(ctx context.Context, r api.RevokeAccountSessionRequestObject) (api.RevokeAccountSessionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = tx.RevokeOwnOtherSessions(u.ID, tokenFrom(ctx), &r.Id); err != nil {
		return nil, err
	}
	return api.RevokeAccountSession204Response{}, nil
}
func (h *Handlers) RevokeOtherAccountSessions(ctx context.Context, _ api.RevokeOtherAccountSessionsRequestObject) (api.RevokeOtherAccountSessionsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = tx.RevokeOwnOtherSessions(u.ID, tokenFrom(ctx), nil); err != nil {
		return nil, err
	}
	return api.RevokeOtherAccountSessions204Response{}, nil
}
func mockChallengeAPI(c *store.MockEmailChange) api.EmailChange {
	return api.EmailChange{Id: c.ID, ExpiresAt: c.ExpiresAt, ResendAt: c.SentAt.Add(time.Minute)}
}
func (h *Handlers) StartEmailChange(ctx context.Context, r api.StartEmailChangeRequestObject) (api.StartEmailChangeResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(string(r.Body.NewEmail)))
	count := 0
	for _, c := range tx.D.EmailChanges {
		if c.UserID == u.ID && c.CreatedAt.After(tx.Now.Add(-time.Hour)) {
			count++
		}
	}
	if count >= 3 {
		return nil, store.SettingsRateLimited("email_change", time.Hour)
	}
	for _, u := range tx.D.Users {
		if u.Email == email {
			return nil, store.ConflictReason("email_taken", "email is already registered", nil)
		}
	}
	for _, c := range tx.D.EmailChanges {
		if c.UserID == u.ID {
			c.Consumed = true
		}
	}
	c := &store.MockEmailChange{ID: store.NewID(), UserID: u.ID, CurrentEmail: u.Email, NewEmail: email, ExpiresAt: tx.Now.Add(10 * time.Minute), SentAt: tx.Now, CreatedAt: tx.Now, Sends: 1}
	tx.D.EmailChanges[c.ID] = c
	return api.StartEmailChange202JSONResponse(mockChallengeAPI(c)), nil
}
func mockChallenge(tx *store.Tx, u *store.User, id uuid.UUID) (*store.MockEmailChange, error) {
	c := tx.D.EmailChanges[id]
	if c == nil || c.UserID != u.ID {
		return nil, store.NotFound("email_change", id)
	}
	if c.Consumed {
		return nil, store.Invalid("code", "consumed_code", "code consumed")
	}
	if !tx.Now.Before(c.ExpiresAt) || u.Email != c.CurrentEmail {
		return nil, store.Invalid("code", "expired_code", "code expired")
	}
	if c.Attempts >= 5 {
		return nil, store.SettingsRateLimited("email_change_attempts", c.ExpiresAt.Sub(tx.Now))
	}
	return c, nil
}
func (h *Handlers) ResendEmailChange(ctx context.Context, r api.ResendEmailChangeRequestObject) (api.ResendEmailChangeResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := mockChallenge(tx, u, r.Id)
	if err != nil {
		return nil, err
	}
	if c.Sends >= 3 {
		return nil, store.SettingsRateLimited("email_change_sends", c.ExpiresAt.Sub(tx.Now))
	}
	if tx.Now.Before(c.SentAt.Add(time.Minute)) {
		return nil, store.SettingsRateLimited("email_change_resend", c.SentAt.Add(time.Minute).Sub(tx.Now))
	}
	c.SentAt = tx.Now
	c.Sends++
	return api.ResendEmailChange202JSONResponse(mockChallengeAPI(c)), nil
}
func (h *Handlers) ConfirmEmailChange(ctx context.Context, r api.ConfirmEmailChangeRequestObject) (api.ConfirmEmailChangeResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := mockChallenge(tx, u, r.Id)
	if err != nil {
		return nil, err
	}
	c.Attempts++
	if r.Body.CurrentCode != "000000" || r.Body.NewCode != "111111" {
		return nil, store.Invalid("code", "invalid_code", "invalid code")
	}
	for _, u := range tx.D.Users {
		if u.Email == c.NewEmail {
			return nil, store.ConflictReason("email_taken", "email is already registered", nil)
		}
	}
	c.Consumed = true
	u.Email = c.NewEmail
	if err = tx.RevokeOwnOtherSessions(u.ID, tokenFrom(ctx), nil); err != nil {
		return nil, err
	}
	return api.ConfirmEmailChange204Response{}, nil
}
func mockNotification(n *store.Notification) api.Notification {
	role := store.RoleAdmin
	if strings.HasPrefix(n.Link, "/adv/") {
		role = store.RoleAdvertiser
	}
	if strings.HasPrefix(n.Link, "/blg/") {
		role = store.RoleBlogger
	}
	group := store.GroupFor(role, n.Template)
	out := api.Notification{Id: n.ID, Template: n.Template, Group: &group, Title: n.Title, Text: n.Text, ObjectType: n.ObjectType, ObjectId: n.ObjectID, OrderId: n.OrderID, IsRead: n.ReadAt != nil, CreatedAt: n.CreatedAt}
	if n.Link != "" {
		link := n.Link
		out.Link = &link
	}
	return out
}
func (h *Handlers) GetNotification(ctx context.Context, r api.GetNotificationRequestObject) (api.GetNotificationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, n := range tx.D.Notifications {
		if n.UserID == u.ID && n.ID == r.Id {
			return api.GetNotification200JSONResponse(mockNotification(n)), nil
		}
	}
	return nil, store.NotFound("notification", r.Id)
}
