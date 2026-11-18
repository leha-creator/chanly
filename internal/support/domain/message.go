package domain

import (
	"github.com/google/uuid"
	"time"
)

const (
	Queued  = "queued"
	Sending = "sending"
	Sent    = "sent"
	Failed  = "failed"
	Unknown = "unknown"
)

type Message struct {
	ID              uuid.UUID    `json:"id"`
	ConversationID  uuid.UUID    `json:"conversation_id"`
	Sequence        int64        `json:"sequence"`
	Direction       string       `json:"direction"`
	Text            string       `json:"text"`
	ActorID         *uuid.UUID   `json:"actor_id,omitempty"`
	Status          string       `json:"status"`
	Reason          string       `json:"reason"`
	ExternalID      string       `json:"external_id"`
	ReplyToSequence int64        `json:"reply_to_sequence"`
	Edited          bool         `json:"edited"`
	Deleted         bool         `json:"deleted"`
	CreatedAt       time.Time    `json:"created_at"`
	Attachments     []Attachment `json:"attachments"`
}

type Attachment struct {
	ID       uuid.UUID `json:"id"`
	Type     string    `json:"type"`
	MIME     string    `json:"mime"`
	Size     int64     `json:"size"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Duration float64   `json:"duration"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	// Private persistence fields never appear in admin DTOs or logs.
	StorageKey    string     `json:"-"`
	OriginalKey   string     `json:"-"`
	ProviderToken string     `json:"-"`
	ProviderURL   string     `json:"-"`
	ActorID       *uuid.UUID `json:"-"`
	MessageID     *uuid.UUID `json:"-"`
}

type Reply struct {
	Text            string      `json:"text"`
	Attachments     []uuid.UUID `json:"attachments"`
	ClientKey       uuid.UUID   `json:"client_key"`
	ReplyToSequence int64       `json:"reply_to_sequence"`
}

type Limits struct {
	Image          int64 `json:"image"`
	Video          int64 `json:"video"`
	Audio          int64 `json:"audio"`
	File           int64 `json:"file"`
	MaxAttachments int   `json:"max_attachments"`
	VoiceSeconds   int   `json:"voice_seconds"`
}

func DefaultLimits() Limits { return Limits{20 << 20, 250 << 20, 50 << 20, 20 << 20, 5, 600} }
func (l Limits) Bytes(kind string) int64 {
	switch kind {
	case "image":
		return l.Image
	case "video":
		return l.Video
	case "audio":
		return l.Audio
	case "file":
		return l.File
	}
	return 0
}
