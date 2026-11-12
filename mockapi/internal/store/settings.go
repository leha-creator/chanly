package store

//go:generate go run ../../../scripts/mock-settings/main.go ../../..

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
)

type Role string

const (
	RoleAdvertiser Role = "advertiser"
	RoleBlogger    Role = "blogger"
	RoleAdmin      Role = "admin"
)

var ErrUnknownTemplate = fmtError("unknown template")

type fmtError string

func (e fmtError) Error() string             { return string(e) }
func settingsConflict(message string) *Error { return Conflict("conflict", message, nil) }
func settingsValidation(field, reason string) *Error {
	return Invalid(field, reason, "request is invalid")
}
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}
func SettingsRateLimited(scope string, after time.Duration) *Error {
	return (&Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests"}).WithDetail("scope", scope).WithDetail("retry_after", int(after.Seconds()))
}

type MockEmailChange struct {
	ID, UserID                   uuid.UUID
	CurrentEmail, NewEmail       string
	ExpiresAt, SentAt, CreatedAt time.Time
	Attempts, Sends              int
	Consumed                     bool
}

func (tx *Tx) AccountSettings(user uuid.UUID) Settings {
	if s, ok := tx.D.Settings[user]; ok {
		return s
	}
	return DefaultSettings()
}
func (tx *Tx) AccountSettingsAccess(u *User) SettingsAccess {
	out := SettingsAccess{}
	if u.AdminRole != "" {
		out.Roles = append(out.Roles, RoleAdmin)
	}
	if u.OrgID != nil {
		o := tx.D.Orgs[*u.OrgID]
		if o != nil {
			if o.IsAdvertiser {
				out.Roles = append(out.Roles, RoleAdvertiser)
			}
			if o.IsBlogger {
				out.Roles = append(out.Roles, RoleBlogger)
			}
			for _, ch := range tx.D.Channels {
				if ch.OrgID != nil && *ch.OrgID == o.ID && ch.BotIsAdmin {
					out.MaxBot = true
				}
			}
		}
	}
	return out
}
func (tx *Tx) OwnSessions(user uuid.UUID, current string) []api.AccountSession {
	out := []api.AccountSession{}
	for token, s := range tx.s.sessions {
		if s.UserID == user && tx.Now.Before(s.ExpiresAt) {
			out = append(out, api.AccountSession{Id: s.ID, Current: token == current, CreatedAt: s.CreatedAt, LastActiveAt: s.CreatedAt, ExpiresAt: s.ExpiresAt})
		}
	}
	return out
}
func (tx *Tx) RevokeOwnOtherSessions(user uuid.UUID, current string, id *uuid.UUID) error {
	found := id == nil
	for token, s := range tx.s.sessions {
		if s.UserID != user {
			continue
		}
		if id != nil && s.ID != *id {
			continue
		}
		if token == current {
			if id != nil {
				return settingsValidation("id", "use_logout")
			}
			continue
		}
		delete(tx.s.sessions, token)
		found = true
	}
	if !found {
		return NotFound("session", id)
	}
	return nil
}
