// Package domain defines support conversations independently of MAX, HTTP and PostgreSQL.
package domain

import (
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

type Conversation struct {
	ID              uuid.UUID  `json:"id"`
	BotID           string     `json:"bot_id"`
	UserID          string     `json:"max_user_id"`
	Name            string     `json:"name"`
	Username        string     `json:"username"`
	Status          string     `json:"status"`
	AssigneeID      *uuid.UUID `json:"assignee_id,omitempty"`
	Version         int64      `json:"version"`
	LastSequence    int64      `json:"last_sequence"`
	LastIncoming    int64      `json:"last_incoming"`
	AnsweredThrough int64      `json:"answered_through"`
	Unread          int        `json:"unread"`
	NeedsReply      bool       `json:"needs_reply"`
	Preview         string     `json:"preview"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type Filter struct {
	Query, Status    string
	Mine, NeedsReply bool
	ActorID          uuid.UUID
	Offset, Limit    int32
}

func ValidateReply(text string, attachments int) error {
	if utf8.RuneCountInString(text) > 4000 {
		return apperr.Validation("text", "too_long")
	}
	if text == "" && attachments == 0 {
		return apperr.Validation("text", "required")
	}
	if attachments > 5 {
		return apperr.Validation("attachments", "too_many")
	}
	return nil
}

func ReplyNeeded(lastIncoming, answeredThrough int64) bool { return lastIncoming > answeredThrough }

func ValidateMediaKinds(kinds []string) error {
	for _, kind := range kinds {
		if kind == "file" || kind == "audio" {
			if len(kinds) > 1 {
				return apperr.Validation("attachments", "incompatible_types")
			}
		}
	}
	return nil
}

func AdvanceRead(current, requested, last int64) int64 {
	if requested > last {
		requested = last
	}
	if requested > current {
		return requested
	}
	return current
}
