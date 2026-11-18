package httpapi

import (
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/support/domain"
)

func value[T any](p *T) (v T) {
	if p != nil {
		return *p
	}
	return
}
func conversation(c domain.Conversation) apigen.SupportConversation {
	return apigen.SupportConversation{Id: c.ID, BotId: c.BotID, MaxUserId: c.UserID, Name: c.Name, Username: c.Username, Status: apigen.SupportConversationStatus(c.Status), AssigneeId: c.AssigneeID, Version: c.Version, LastSequence: c.LastSequence, LastIncoming: c.LastIncoming, AnsweredThrough: c.AnsweredThrough, Unread: c.Unread, NeedsReply: c.NeedsReply, Preview: c.Preview, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}
func attachment(a domain.Attachment) apigen.SupportAttachment {
	return apigen.SupportAttachment{Id: a.ID, Type: apigen.SupportAttachmentType(a.Type), Mime: a.MIME, Size: a.Size, Name: a.Name, Status: apigen.SupportAttachmentStatus(a.Status), Duration: float32(a.Duration), Width: a.Width, Height: a.Height}
}
func message(m domain.Message) apigen.SupportMessage {
	a := make([]apigen.SupportAttachment, 0, len(m.Attachments))
	for _, v := range m.Attachments {
		a = append(a, attachment(v))
	}
	return apigen.SupportMessage{Id: m.ID, ConversationId: m.ConversationID, Sequence: m.Sequence, Direction: apigen.SupportMessageDirection(m.Direction), Text: m.Text, ActorId: m.ActorID, Status: apigen.SupportMessageStatus(m.Status), Reason: m.Reason, ExternalId: m.ExternalID, ReplyToSequence: m.ReplyToSequence, Edited: m.Edited, Deleted: m.Deleted, CreatedAt: m.CreatedAt, Attachments: a}
}
