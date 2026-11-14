package postgres_test

import (
	"context"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
	"testing"
)

func TestCatalogPlatformFee25SQLParity(t *testing.T) {
	st, d := newStore(t)
	id := seed(t, d, spec{prices: map[string]int64{"f_1_24": 10_007, "f_2_48": 1_000_000_000}, disabled: map[string]int64{"f_3_72": 10_000}, avgViews: p(100)})
	ctx := context.Background()
	card, err := st.Catalog().Get(ctx, id, 2500, rkn)
	if err != nil || len(card.Prices) != 2 {
		t.Fatalf("card: %+v %v", card, err)
	}
	for _, price := range card.Prices {
		want := money.Kopecks(12_508)
		if price.FormatID == "f_2_48" {
			want = 1_250_000_000
		}
		if price.Total != want {
			t.Fatalf("price: %+v want %d", price, want)
		}
	}
	boundary := money.Kopecks(12_508)
	for _, sort := range []domain.CatalogSort{domain.SortPriceAsc, domain.SortPriceDesc, domain.SortCPVAsc} {
		items, err := st.Catalog().List(ctx, domain.CatalogQuery{FeeRateBP: 2500, RknThreshold: rkn, Sort: sort, Limit: 10, Filter: domain.CatalogFilter{PriceFrom: &boundary, PriceTo: &boundary}})
		if err != nil || len(items) != 1 || items[0].ID != id || items[0].CPV == nil || *items[0].CPV != 125 {
			t.Fatalf("sort=%s: %+v %v", sort, items, err)
		}
	}
	boundary--
	items, err := st.Catalog().List(ctx, domain.CatalogQuery{FeeRateBP: 2500, RknThreshold: rkn, Sort: domain.SortPriceAsc, Limit: 10, Filter: domain.CatalogFilter{PriceTo: &boundary}})
	if err != nil || len(items) != 0 {
		t.Fatalf("exclusive boundary: %+v %v", items, err)
	}
	boundary = 1_250_000_000
	items, err = st.Catalog().List(ctx, domain.CatalogQuery{FeeRateBP: 2500, RknThreshold: rkn, Sort: domain.SortPriceAsc, Limit: 10, Filter: domain.CatalogFilter{FormatID: "f_2_48", PriceFrom: &boundary, PriceTo: &boundary}})
	if err != nil || len(items) != 1 {
		t.Fatalf("selected format maximum: %+v %v", items, err)
	}
}
