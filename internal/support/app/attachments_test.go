package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/support/domain"
)

type mediaRepo struct {
	Repository
	attachment domain.Attachment
}

func (r *mediaRepo) Attachment(context.Context, uuid.UUID) (domain.Attachment, error) {
	return r.attachment, nil
}
func (r *mediaRepo) ClaimMedia(context.Context, uuid.UUID, time.Time) (bool, error) {
	r.attachment.Status = "processing"
	return true, nil
}
func (r *mediaRepo) SaveAttachment(_ context.Context, attachment domain.Attachment) error {
	r.attachment = attachment
	return nil
}

type mediaRoundTrip func(*http.Request) (*http.Response, error)

func (fn mediaRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestIncomingMediaFromDomainFamily(t *testing.T) {
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"i.oneme.ru", "new.region.oneme.ru", "new.cdn.okcdn.ru", "oneme.ru.evil.test"} {
		t.Run(host, func(t *testing.T) {
			ctx := context.Background()
			privateURL := "https://" + host + "/private-image?signature=private-signature"
			repo := &mediaRepo{attachment: domain.Attachment{ID: ids.New(), Type: "image", Status: "pending", ProviderURL: privateURL, ProviderToken: "private-provider-token"}}
			var logs bytes.Buffer
			storage := storagefake.New()
			requests := 0
			service := &Service{Repo: repo, Storage: storage, Clock: clock.System{}, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Limits: domain.DefaultLimits(), MediaHosts: []string{"oneme.ru", "*.oneme.ru", "okcdn.ru", "*.okcdn.ru"}}
			service.Fetch = &http.Client{Transport: mediaRoundTrip(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.Header.Get("Authorization") != "" {
					t.Fatal("bot authorization forwarded to CDN")
				}
				return &http.Response{StatusCode: 200, ContentLength: int64(content.Len()), Body: io.NopCloser(bytes.NewReader(content.Bytes()))}, nil
			})}
			if err := service.PrepareMedia(ctx, repo.attachment.ID); err != nil {
				t.Fatal(err)
			}
			if host == "oneme.ru.evil.test" {
				if requests != 0 || repo.attachment.Status != "failed" || !strings.Contains(logs.String(), "origin_not_allowed") {
					t.Fatal("untrusted media origin was fetched or lacked a safe failure reason")
				}
			} else {
				attachment := repo.attachment
				if requests != 1 || attachment.Status != "ready" || attachment.MIME != "image/png" || attachment.ProviderURL != "" || attachment.ProviderToken != "" {
					t.Fatal("trusted incoming image was not prepared")
				}
				body, size, err := storage.Get(ctx, attachment.StorageKey)
				if err != nil {
					t.Fatal(err)
				}
				body.Close()
				if size != int64(content.Len()) {
					t.Fatal("private image size mismatch")
				}
			}
			for _, secret := range []string{privateURL, "private-signature", "private-provider-token", "/private-image"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("private media source leaked into logs")
				}
			}
		})
	}
}
