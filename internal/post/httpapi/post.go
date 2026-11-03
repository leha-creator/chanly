package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/post/app"
	"github.com/leha-creator/chanly/internal/post/domain"
)

func (h *Handlers) GetCampaignPost(ctx context.Context, req apigen.GetCampaignPostRequestObject) (apigen.GetCampaignPostResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.GetDraft(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetCampaignPost200JSONResponse(toPostDraft(d)), nil
}

func (h *Handlers) SaveCampaignPost(ctx context.Context, req apigen.SaveCampaignPostRequestObject) (apigen.SaveCampaignPostResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	b := apigen.PostDraftInput(*req.Body)
	in := app.DraftInput{Doc: fromEditorDoc(b.ContentDoc), MediaIDs: b.MediaFileIds}
	if b.TokenCarrier != nil {
		c := domain.TokenCarrier(*b.TokenCarrier)
		in.TokenCarrier = &c
	}
	if b.TokenPosition != nil {
		p := domain.TokenPosition(*b.TokenPosition)
		in.TokenPosition = &p
	}
	d, err := h.svc.SaveDraft(ctx, org, user, req.Id, in)
	if err != nil {
		return nil, err
	}
	return apigen.SaveCampaignPost200JSONResponse(toPostDraft(d)), nil
}

func (h *Handlers) PreviewCampaignPost(ctx context.Context, req apigen.PreviewCampaignPostRequestObject) (apigen.PreviewCampaignPostResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Preview(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	problems := make([]apigen.PostProblem, 0, len(v.Problems))
	for _, p := range v.Problems {
		problems = append(problems, apigen.PostProblem{Code: apigen.PostProblemCode(p.Code), Message: p.Message})
	}
	return apigen.PreviewCampaignPost200JSONResponse{
		Body: v.Body, BodyLength: v.BodyLength, BodyLimit: v.BodyLimit, FinalTextSample: v.FinalTextSample,
		MediaUrls: v.MediaURLs, Problems: problems,
	}, nil
}
