package domain

import (
	"context"
	"github.com/google/uuid"
	"io"
	"time"
)

// Store methods participate in the transaction passed in ctx.
type Store interface {
	List(context.Context, Filter) ([]Conversation, int, error)
	Conversation(context.Context, uuid.UUID, uuid.UUID, bool) (Conversation, error)
	Messages(context.Context, uuid.UUID, int64, int32) ([]Message, error)
	Message(context.Context, uuid.UUID, bool) (Message, error)
	Attachment(context.Context, uuid.UUID) (Attachment, error)
	SaveAttachment(context.Context, Attachment) error
	QueueReply(context.Context, uuid.UUID, uuid.UUID, Reply, time.Time) (Message, bool, error)
	MarkRead(context.Context, uuid.UUID, uuid.UUID, int64) error
	Update(context.Context, uuid.UUID, int64, string, *uuid.UUID, time.Time) error
	Claim(context.Context, uuid.UUID, time.Time) (Message, bool, error)
	Complete(context.Context, Message, string, string, string, time.Time) error
	Retry(context.Context, uuid.UUID, time.Time) error
	Waiting(context.Context) (int, error)
}

// Provider sends only from worker jobs, outside transactions.
type Provider interface {
	Send(context.Context, string, string, []Attachment) (string, error)
	Upload(context.Context, string, string, io.Reader) (string, error)
	ResolveVideo(context.Context, string) (string, error)
}

// DeliveryError classifies outcomes without carrying private response bodies.
type DeliveryError struct {
	Reason     string
	Definitive bool
	Temporary  bool
}

func (e *DeliveryError) Error() string { return "support delivery: " + e.Reason }
