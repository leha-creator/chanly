package httpapi

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/post/app"
	"github.com/leha-creator/chanly/internal/post/domain"
)

func toMediaFile(v app.MediaView) apigen.MediaFile {
	f := v.File
	return apigen.MediaFile{
		Id: f.ID, OriginalName: f.OriginalName, Mime: apigen.MediaFileMime(f.Mime), SizeBytes: f.SizeBytes,
		Width: f.Width, Height: f.Height, PreviewUrl: v.PreviewURL, CreatedAt: f.CreatedAt,
	}
}

func toDate(d *domain.Date) *openapi_types.Date {
	if d == nil {
		return nil
	}
	return &openapi_types.Date{Time: *d}
}

func fromDate(d *openapi_types.Date) *domain.Date {
	if d == nil {
		return nil
	}
	v := domain.NewDate(d.Year(), d.Month(), d.Day())
	return &v
}

func toTime(t *domain.TimeOfDay) *apigen.TimeOfDay {
	if t == nil {
		return nil
	}
	s := t.String()
	return &s
}

func toCampaign(c domain.Campaign) apigen.Campaign {
	return apigen.Campaign{
		Id: c.ID, Name: c.Name, Status: apigen.CampaignStatus(c.Status),
		DateFrom: toDate(c.DateFrom), DateTo: toDate(c.DateTo), TimeFrom: toTime(c.TimeFrom), TimeTo: toTime(c.TimeTo),
		ExtraRequirements: c.ExtraRequirements, OrderId: c.OrderID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// fromCampaignInput переводит форму кампании; время суток уже проверено схемой.
func fromCampaignInput(in apigen.CampaignInput) (domain.CampaignInput, error) {
	out := domain.CampaignInput{Name: in.Name, DateFrom: fromDate(in.DateFrom), DateTo: fromDate(in.DateTo),
		ExtraRequirements: in.ExtraRequirements}
	for _, f := range []struct {
		field string
		src   *string
		dst   **domain.TimeOfDay
	}{{"time_from", in.TimeFrom, &out.TimeFrom}, {"time_to", in.TimeTo, &out.TimeTo}} {
		if f.src == nil {
			continue
		}
		t, err := domain.ParseTimeOfDay(*f.src)
		if err != nil {
			return domain.CampaignInput{}, apperr.Validation(f.field, "invalid").WithCause(err)
		}
		*f.dst = &t
	}
	return out, nil
}

func fromEditorDoc(in apigen.EditorDoc) domain.Doc {
	d := domain.Doc{Blocks: make([]domain.Block, 0, len(in.Content))}
	for _, b := range in.Content {
		block := domain.Block{Type: domain.BlockType(b.Type)}
		for _, t := range deref(b.Content) {
			text := domain.Text{Text: t.Text}
			for _, m := range deref(t.Marks) {
				text.Marks = append(text.Marks, domain.Mark{Type: domain.MarkType(m.Type), Href: deref(m.Href)})
			}
			block.Content = append(block.Content, text)
		}
		d.Blocks = append(d.Blocks, block)
	}
	return d
}

func toEditorDoc(d domain.Doc) apigen.EditorDoc {
	out := apigen.EditorDoc{Type: apigen.Doc, Content: make([]apigen.EditorBlock, 0, len(d.Blocks))}
	for _, b := range d.Blocks {
		block := apigen.EditorBlock{Type: apigen.EditorBlockType(b.Type)}
		if len(b.Content) > 0 {
			nodes := make([]apigen.EditorText, 0, len(b.Content))
			for _, t := range b.Content {
				node := apigen.EditorText{Type: apigen.EditorTextTypeText, Text: t.Text}
				if len(t.Marks) > 0 {
					marks := make([]apigen.EditorMark, 0, len(t.Marks))
					for _, m := range t.Marks {
						marks = append(marks, apigen.EditorMark{Type: apigen.EditorMarkType(m.Type), Href: opt(m.Href)})
					}
					node.Marks = &marks
				}
				nodes = append(nodes, node)
			}
			block.Content = &nodes
		}
		out.Content = append(out.Content, block)
	}
	return out
}

func toPostDraft(v app.DraftView) apigen.PostDraft {
	media := make([]apigen.MediaFile, 0, len(v.Media))
	for _, m := range v.Media {
		media = append(media, toMediaFile(m))
	}
	p := v.Post
	return apigen.PostDraft{
		CampaignId: p.CampaignID, ContentDoc: toEditorDoc(p.Draft.Doc), Media: media,
		TokenCarrier: apigen.TokenCarrier(p.Draft.TokenCarrier), TokenPosition: apigen.TokenPosition(p.Draft.TokenPosition),
		UpdatedAt: p.UpdatedAt,
	}
}
