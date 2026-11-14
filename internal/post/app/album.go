package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

type albumRequirement struct {
	Position int
	FileID   uuid.UUID
	Params   domain.DerivativeParams
}

type albumDerivativeKey struct {
	FileID uuid.UUID
	Kind   domain.DerivativeKind
	Hash   string
}

func (s *Service) draftRequirements(ctx context.Context, orgID uuid.UUID, d domain.Draft) ([]albumRequirement, domain.Requisites, bool, error) {
	req, err := s.Requisites.Requisites(ctx, orgID)
	if err != nil {
		return nil, domain.Requisites{}, false, err
	}
	items := make([]albumRequirement, 0, len(d.MediaIDs))
	for position, id := range d.MediaIDs {
		kind := domain.NeededKindAt(d.TokenCarrier, position)
		items = append(items, albumRequirement{Position: position, FileID: id,
			Params: domain.NewDerivativeParams(kind, req.Name, req.INN)})
	}
	canBuild := d.TokenCarrier != domain.CarrierMedia || req.Complete() || len(items) == 0
	return items, req, canBuild, nil
}

// albumDerivatives resolves exact requirements in at most two batched queries.
// Never replace an unavailable selected variant with another ready variant.
func (s *Service) albumDerivatives(ctx context.Context, orgID uuid.UUID, items []albumRequirement, canBuild bool) ([]domain.Derivative, []albumRequirement, bool, error) {
	selected := make([]domain.Derivative, len(items))
	if len(items) == 0 {
		return selected, nil, true, nil
	}
	if !canBuild {
		return selected, nil, false, nil
	}
	ids := make([]uuid.UUID, len(items))
	groups := map[domain.DerivativeKind][]uuid.UUID{}
	for i, item := range items {
		ids[i] = item.FileID
		groups[item.Params.Kind] = append(groups[item.Params.Kind], item.FileID)
	}
	live, err := s.MediaFiles.LiveSubset(ctx, orgID, ids)
	if err != nil {
		return nil, nil, false, err
	}
	liveIDs := make(map[uuid.UUID]bool, len(live))
	for _, f := range live {
		liveIDs[f.ID] = true
	}
	byKey := map[albumDerivativeKey]domain.Derivative{}
	for _, kind := range []domain.DerivativeKind{domain.KindLabeled, domain.KindPlain} {
		if len(groups[kind]) == 0 {
			continue
		}
		list, err := s.Derivatives.ListByFiles(ctx, groups[kind], kind)
		if err != nil {
			return nil, nil, false, err
		}
		for _, d := range list {
			if d.Kind == kind && d.Ready() {
				byKey[albumDerivativeKey{d.MediaFileID, d.Kind, d.ParamsHash}] = d
			}
		}
	}
	ready := true
	missing := []albumRequirement{}
	for i, item := range items {
		if !liveIDs[item.FileID] {
			ready = false
			continue
		}
		d, ok := byKey[albumDerivativeKey{item.FileID, item.Params.Kind, item.Params.Hash()}]
		if !ok {
			ready = false
			missing = append(missing, item)
			continue
		}
		selected[i] = d
		s.log.DebugContext(ctx, "album derivative selected", logx.KeyOp, "Post.SelectAlbum", "position", item.Position,
			"kind", d.Kind, KeyDerivativeID, d.ID, "render_version", item.Params.Version())
	}
	s.log.DebugContext(ctx, "album derivatives resolved", logx.KeyOp, "Post.SelectAlbum", "media_count", len(items),
		"missing_count", len(missing), "ready", ready)
	return selected, missing, ready, nil
}
