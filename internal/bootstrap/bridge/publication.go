package bridge

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/integration/max"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// ---------------------------------------------------------------- placement → MAX

// MAXPosts — методы адаптера MAX для публикации и контроля (integration/max, maxfake).
type MAXPosts interface {
	UploadImage(ctx context.Context, chatID int64, r io.Reader, size int64, contentType string) (string, error)
	SendChannelPost(ctx context.Context, chatID int64, text string, imageTokens []string, rec max.Recovery) (max.PostedMessage, error)
	GetMessage(ctx context.Context, messageID string) (max.ChannelMessage, error)
	DeleteMessage(ctx context.Context, chatID int64, messageID string) error
	ListMessages(ctx context.Context, chatID int64, from, to time.Time, count int) ([]max.ChannelMessage, error)
	FindPost(ctx context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*max.ChannelMessage, error)
	BotUserID(ctx context.Context) (int64, error)
}

// PlacementPublisher — placement/domain.ChannelPublisher поверх адаптера MAX (R-1): приоритет
// ограничителя из context (R-15), «сообщения нет» и коды отказов — на языке модуля.
type PlacementPublisher struct{ API MAXPosts }

var _ domain.ChannelPublisher = PlacementPublisher{}

func priority(ctx context.Context) context.Context {
	if domain.CallPriorityFrom(ctx) == domain.CallPublish {
		return max.WithPriority(ctx, ratelimit.PriorityPublish)
	}
	return max.WithPriority(ctx, ratelimit.PriorityReconcile)
}

// mapErr — сообщения нет → domain.ErrMessageNotFound; отказ MAX без повторов — с его кодом.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, max.ErrMessageNotFound) {
		return domain.ErrMessageNotFound
	}
	if e, ok := max.AsAPIError(err); ok && job.IsPermanent(err) {
		return job.Permanent(&domain.ExternalError{Code: e.Code, Err: err})
	}
	return err
}

func message(m max.ChannelMessage) domain.ChannelMessage {
	return domain.ChannelMessage{ID: m.MID, At: m.At, SenderID: m.SenderID, ContentHash: m.ContentHash, Views: m.Views}
}

func (b PlacementPublisher) UploadImage(ctx context.Context, chatID int64, r io.Reader, size int64, contentType string) (string, error) {
	token, err := b.API.UploadImage(priority(ctx), chatID, r, size, contentType)
	return token, mapErr(err)
}

func (b PlacementPublisher) SendPost(ctx context.Context, chatID int64, p domain.OutgoingPost) (domain.SentMessage, error) {
	m, err := b.API.SendChannelPost(priority(ctx), chatID, p.Text, p.ImageTokens, max.Recovery{
		SenderID: p.RecoverSenderID, Since: p.RecoverSince, Needle: p.RecoverNeedle,
	})
	if err != nil {
		return domain.SentMessage{}, mapErr(err)
	}
	return domain.SentMessage{ID: m.ID, URL: m.URL, At: m.At, ContentHash: m.ContentHash}, nil
}

func (b PlacementPublisher) GetMessage(ctx context.Context, messageID string) (domain.ChannelMessage, error) {
	m, err := b.API.GetMessage(priority(ctx), messageID)
	if err != nil {
		return domain.ChannelMessage{}, mapErr(err)
	}
	return message(m), nil
}

func (b PlacementPublisher) DeleteMessage(ctx context.Context, chatID int64, messageID string) error {
	return mapErr(b.API.DeleteMessage(priority(ctx), chatID, messageID))
}

func (b PlacementPublisher) ListMessages(ctx context.Context, chatID int64, from, to time.Time) ([]domain.ChannelMessage, error) {
	list, err := b.API.ListMessages(priority(ctx), chatID, from, to, max.MaxListCount)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]domain.ChannelMessage, 0, len(list))
	for _, m := range list {
		out = append(out, message(m))
	}
	return out, nil
}

func (b PlacementPublisher) FindPost(ctx context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*domain.ChannelMessage, error) {
	m, err := b.API.FindPost(priority(ctx), chatID, from, to, senderID, needle)
	if err != nil || m == nil {
		return nil, mapErr(err)
	}
	out := message(*m)
	return &out, nil
}

func (b PlacementPublisher) BotUserID(ctx context.Context) (int64, error) {
	id, err := b.API.BotUserID(ctx)
	return id, mapErr(err)
}

// ---------------------------------------------------------------- placement ← post

// PlacementContent — placement/domain.PublicationContent поверх сервиса модуля post (R-1).
type PlacementContent struct{ Svc *postapp.Service }

var _ domain.PublicationContent = PlacementContent{}

func (b PlacementContent) Get(ctx context.Context, versionID uuid.UUID) (domain.Content, error) {
	pc, err := b.Svc.PublicationContent(ctx, versionID)
	if err != nil {
		return domain.Content{}, err
	}
	v := pc.Version
	out := domain.Content{VersionID: v.ID, Status: domain.VersionStatus(v.Status), Erid: v.Erid, FinalText: v.FinalText,
		MarkingOK: pc.MarkingOK && v.Status == postdomain.VersionReady}
	for _, m := range pc.Media {
		out.Media = append(out.Media, domain.MediaRef{DerivativeID: m.DerivativeID, ContentType: m.ContentType, Size: m.Size})
	}
	return out, nil
}

func (b PlacementContent) Open(ctx context.Context, derivativeID uuid.UUID) (io.ReadCloser, error) {
	return b.Svc.OpenDerivative(ctx, derivativeID)
}

// ---------------------------------------------------------------- channel → placement

// ChannelPosts — channel/domain.ChannelPostListener поверх сервиса размещений (R-9): контроль
// «часов в топе» по сообщениям каналов.
type ChannelPosts struct{ Svc *placementapp.Service }

var _ channeldomain.ChannelPostListener = ChannelPosts{}

func (b ChannelPosts) ChannelPostCreated(ctx context.Context, p channeldomain.ChannelPost) error {
	return b.Svc.ChannelPost(ctx, domain.ChannelPost{
		ChannelID: p.ChannelID, MaxChatID: p.MaxChatID, MessageID: p.MessageID, SenderID: p.SenderID, At: p.At,
		InboundEventID: p.InboundEventID,
	})
}
