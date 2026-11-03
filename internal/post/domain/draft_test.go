package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestSaveDraft(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	a, b := uuid.New(), uuid.New()
	text := doc(p(txt("Текст")))
	tests := []struct {
		name     string
		in       domain.DraftInput
		err      error
		carrier  domain.TokenCarrier
		position domain.TokenPosition
	}{
		{"defaults with images", domain.DraftInput{Doc: text, MediaIDs: []uuid.UUID{a}}, nil, domain.CarrierMedia, domain.PositionBelow},
		{"defaults without images", domain.DraftInput{Doc: text}, nil, domain.CarrierText, domain.PositionBelow},
		{"explicit", domain.DraftInput{Doc: text, MediaIDs: []uuid.UUID{a}, TokenCarrier: ptr(domain.CarrierText),
			TokenPosition: ptr(domain.PositionAbove)}, nil, domain.CarrierText, domain.PositionAbove},
		// Недоступный выбор не отклоняется — его покажет предпросмотр (R-13).
		{"media without images kept", domain.DraftInput{Doc: text, TokenCarrier: ptr(domain.CarrierMedia)}, nil, domain.CarrierMedia, domain.PositionBelow},
		{"duplicate", domain.DraftInput{Doc: text, MediaIDs: []uuid.UUID{a, b, a}}, domain.ErrDuplicateMedia, "", ""},
		{"too many", domain.DraftInput{Doc: text, MediaIDs: make11()}, domain.ErrTooManyMedia, "", ""},
		{"too long", domain.DraftInput{Doc: doc(p(txt(strings.Repeat("я", 8001))))}, domain.ErrDraftTooLong, "", ""},
		{"long but under save limit", domain.DraftInput{Doc: doc(p(txt(strings.Repeat("я", 8000))))}, nil, domain.CarrierText, domain.PositionBelow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			post := domain.NewPost(uuid.New(), uuid.New(), uuid.New(), now)
			err := post.SaveDraft(tt.in, domain.DefaultAlbumLimit, now.Add(time.Minute))
			if !errors.Is(err, tt.err) {
				t.Fatalf("got %v, want %v", err, tt.err)
			}
			if err != nil {
				if post.Draft.TokenCarrier != domain.CarrierText || len(post.Draft.MediaIDs) != 0 {
					t.Fatal("failed save changed the draft")
				}
				return
			}
			if post.Draft.TokenCarrier != tt.carrier || post.Draft.TokenPosition != tt.position {
				t.Fatalf("token %s/%s, want %s/%s", post.Draft.TokenCarrier, post.Draft.TokenPosition, tt.carrier, tt.position)
			}
			if !post.UpdatedAt.Equal(now.Add(time.Minute)) {
				t.Fatal("updated_at")
			}
		})
	}
}

func make11() []uuid.UUID {
	out := make([]uuid.UUID, 11)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

func TestNewPostAndRemoveMedia(t *testing.T) {
	post := domain.NewPost(uuid.New(), uuid.New(), uuid.New(), time.Now())
	if post.Draft.TokenCarrier != domain.CarrierText || post.Draft.TokenPosition != domain.PositionBelow || post.Status != domain.PostDraft {
		t.Fatalf("%+v", post)
	}
	a, b := uuid.New(), uuid.New()
	post.Draft.MediaIDs = []uuid.UUID{a, b}
	if !post.RemoveMedia(a) || len(post.Draft.MediaIDs) != 1 || post.Draft.MediaIDs[0] != b {
		t.Fatalf("remove: %v", post.Draft.MediaIDs)
	}
	if post.RemoveMedia(a) {
		t.Fatal("removed twice")
	}
}
