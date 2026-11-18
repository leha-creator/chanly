package bridge

import (
	"context"
	"errors"
	adminapp "github.com/leha-creator/chanly/internal/admin/app"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/support/domain"
	"io"
)

type SupportMAX struct{ Client *max.Client }

type SupportCounter struct {
	Store interface {
		Waiting(context.Context) (int, error)
	}
	Enabled bool
}

func (SupportCounter) Name() string { return "support" }
func (c SupportCounter) Count(ctx context.Context, out *adminapp.Counters) error {
	if !c.Enabled {
		return nil
	}
	n, e := c.Store.Waiting(ctx)
	out.SupportWaiting = n
	return e
}

func (p SupportMAX) Send(ctx context.Context, userID, text string, attachments []domain.Attachment) (string, error) {
	if p.Client == nil {
		return "fake-support-" + ids.New().String(), nil
	}
	media := make([]max.SupportMedia, 0, len(attachments))
	for _, a := range attachments {
		media = append(media, max.SupportMedia{Type: a.Type, Token: a.ProviderToken})
	}
	mid, e := p.Client.SendSupportMessage(ctx, userID, text, media)
	var d *max.SupportDeliveryError
	if errors.As(e, &d) {
		return "", &domain.DeliveryError{Reason: d.Reason, Definitive: d.Definitive, Temporary: d.Temporary}
	}
	return mid, e
}
func (p SupportMAX) Upload(ctx context.Context, kind, name string, r io.Reader) (string, error) {
	if p.Client == nil {
		_, e := io.Copy(io.Discard, r)
		return "fake-upload-" + ids.New().String(), e
	}
	token, err := p.Client.UploadSupportMedia(ctx, kind, name, r)
	var refusal *max.SupportDeliveryError
	if errors.As(err, &refusal) {
		return "", &domain.DeliveryError{Reason: refusal.Reason, Definitive: refusal.Definitive, Temporary: refusal.Temporary}
	}
	return token, err
}
func (p SupportMAX) ResolveVideo(ctx context.Context, token string) (string, error) {
	if p.Client == nil {
		return "", errors.New("fake video source missing")
	}
	return p.Client.ResolveSupportVideo(ctx, token)
}
