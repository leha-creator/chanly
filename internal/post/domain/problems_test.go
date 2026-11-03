package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestProblems(t *testing.T) {
	req := domain.Requisites{Name: "ООО Ромашка", INN: "7707083893"}
	pc := domain.ProblemContext{BodyLimit: 3800, FinalTextLimit: 4000, AlbumLimit: 10, MediaReady: true, Requisites: req}
	clean := domain.Draft{Doc: doc(p(txt("Текст "), txt("ссылка", link("https://a.test")))), MediaIDs: []uuid.UUID{uuid.New()},
		TokenCarrier: domain.CarrierMedia, TokenPosition: domain.PositionBelow}
	with := func(f func(*domain.Draft, *domain.ProblemContext)) (domain.Draft, domain.ProblemContext) {
		d, c := clean, pc
		f(&d, &c)
		return d, c
	}
	tests := []struct {
		name string
		f    func(*domain.Draft, *domain.ProblemContext)
		body string
		want []string
	}{
		{"clean", func(*domain.Draft, *domain.ProblemContext) {}, "", nil},
		{"empty text", func(d *domain.Draft, _ *domain.ProblemContext) { d.Doc = doc(p(txt("  ")), p()) }, "", []string{"empty_text"}},
		{"body too long", func(*domain.Draft, *domain.ProblemContext) {}, strings.Repeat("я", 3801), []string{"body_too_long"}},
		{"marking line overflows", func(d *domain.Draft, c *domain.ProblemContext) {
			d.TokenCarrier, c.Requisites.Name = domain.CarrierText, strings.Repeat("н", 400)
		}, strings.Repeat("я", 3700), []string{"body_too_long"}},
		{"too many media", func(_ *domain.Draft, c *domain.ProblemContext) { c.AlbumLimit = 0 }, "", []string{"too_many_media"}},
		{"link not https", func(d *domain.Draft, _ *domain.ProblemContext) {
			d.Doc = doc(p(txt("a", link("http://a.test"))))
		}, "", []string{"link_not_https"}},
		{"media required", func(d *domain.Draft, _ *domain.ProblemContext) { d.MediaIDs = nil }, "", []string{"media_required_for_token"}},
		{"link required", func(d *domain.Draft, _ *domain.ProblemContext) {
			d.Doc, d.TokenPosition = doc(p(txt("без ссылок"))), domain.PositionLink
		}, "", []string{"link_required_for_token"}},
		{"media processing", func(_ *domain.Draft, c *domain.ProblemContext) { c.MediaReady = false }, "", []string{"media_processing"}},
		{"processing ignored without media", func(d *domain.Draft, c *domain.ProblemContext) {
			d.MediaIDs, d.TokenCarrier, c.MediaReady = nil, domain.CarrierText, false
		}, "", nil},
		{"requisites missing", func(_ *domain.Draft, c *domain.ProblemContext) { c.Requisites.INN = "" }, "", []string{"requisites_missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, c := with(tt.f)
			body := tt.body
			if body == "" {
				body = domain.Serialize(d.Doc, &domain.PreviewUTM)
			}
			got := domain.Codes(domain.Problems(d, body, c))
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
