package handlers

import (
	"context"
	"slices"
	"strings"

	"maxim/mockapi/internal/api"
)

func (h *Handlers) ListAdminChannelCategories(ctx context.Context, _ api.ListAdminChannelCategoriesRequestObject) (api.ListAdminChannelCategoriesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := admin(ctx, tx); err != nil {
		return nil, err
	}
	items := []api.AdminChannelCategory{}
	for _, c := range tx.D.Categories {
		items = append(items, api.AdminChannelCategory{Id: c.ID, Name: c.Name, Slug: c.Slug, Archived: c.Archived, Position: c.Position})
	}
	slices.SortFunc(items, func(a, b api.AdminChannelCategory) int {
		if a.Position < b.Position {
			return -1
		}
		if a.Position > b.Position {
			return 1
		}
		return strings.Compare(a.Id.String(), b.Id.String())
	})
	return api.ListAdminChannelCategories200JSONResponse{Items: items}, nil
}
