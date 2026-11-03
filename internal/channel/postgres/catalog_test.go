package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var categoryTech = uuid.MustParse("0190a000-0000-7000-8000-000000000202")

const (
	fee = 1500
	rkn = 10000
)

// spec — канал каталога для тестов, записанный прямо в базу одной транзакцией.
type spec struct {
	title, desc string
	category    uuid.UUID
	status      string
	subs        int
	rating      int
	rknStatus   string
	prices      map[string]int64 // включённые цены
	disabled    map[string]int64
	avgViews    *int
	engagement  *int
}

var chatSeq int64 = -71000000000

func seed(t *testing.T, d *dbtest.DB, s spec) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	org := channeltest.Organization(t, d.Pool)
	if s.status == "" {
		s.status = "active"
	}
	if s.category == uuid.Nil {
		s.category = channeltest.CategoryFood
	}
	if s.rknStatus == "" {
		s.rknStatus = "not_required"
	}
	if s.rating == 0 {
		s.rating = 100
	}
	if s.title == "" {
		s.title = "Канал"
	}
	chatSeq--
	id := ids.New()
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO channel (id, organization_id, max_chat_id, title, description, is_public,
		added_by_max_user_id, category_id, status, bot_is_admin, bot_permissions, subscribers_count, rkn_status, rating,
		ord_platform_ready_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, true, 7, $6, $7, true, ARRAY['write', 'delete'], $8, $9, $10, now(), now(), now())`,
		id, org, chatSeq, s.title, s.desc, s.category, s.status, s.subs, s.rknStatus, s.rating); err != nil {
		t.Fatal(err)
	}
	for set, enabled := range map[*map[string]int64]bool{&s.prices: true, &s.disabled: false} {
		for f, amount := range *set {
			if _, err := tx.Exec(ctx, `INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, now(), now())`, ids.New(), id, f, amount, enabled); err != nil {
				t.Fatal(err)
			}
		}
	}
	if s.avgViews != nil || s.engagement != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_metrics (id, channel_id, date, subscribers_count, avg_views, engagement_bp,
			anomaly_flags, created_at, updated_at) VALUES ($1, $2, '2026-10-15', $3, $4, $5, '{}', now(), now())`,
			ids.New(), id, s.subs, s.avgViews, s.engagement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func p(v int) *int { return &v }

func price(v int64) map[string]int64 { return map[string]int64{"f_1_24": v} }

func list(t *testing.T, st *postgres.Store, f domain.CatalogFilter, sort domain.CatalogSort) []uuid.UUID {
	t.Helper()
	items, err := st.Catalog().List(context.Background(), domain.CatalogQuery{Filter: f, Sort: sort, Limit: 100, FeeRateBP: fee, RknThreshold: rkn})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestCatalogVisibilityAndFilters(t *testing.T) {
	st, d := newStore(t)
	food := seed(t, d, spec{title: "Рецепты 100%", desc: "Супы", subs: 5000, prices: map[string]int64{"f_1_24": 300000, "f_2_48": 500000}})
	tech := seed(t, d, spec{title: "Гаджеты", desc: "обзоры_новинок", category: categoryTech, subs: 9000, prices: map[string]int64{"f_2_48": 100000}})
	confirmed := seed(t, d, spec{title: "Большой", subs: 20000, rknStatus: "confirmed", prices: price(1000000)})
	// Не видны: без цены формата, в moderation, 10 000 без подтверждения.
	seed(t, d, spec{title: "Модерация", status: "moderation", prices: price(200000)})
	seed(t, d, spec{title: "Без РКН", subs: 10000, rknStatus: "pending", prices: price(200000)})

	// R-14 этапа «Маркировка»: без площадки в ОРД канал не виден.
	noPlatform := seed(t, d, spec{title: "Без площадки", subs: 100, prices: price(200000)})
	if _, err := d.Pool.Exec(context.Background(), `UPDATE channel SET ord_platform_ready_at = NULL WHERE id = $1`, noPlatform); err != nil {
		t.Fatal(err)
	}

	all := list(t, st, domain.CatalogFilter{}, domain.SortSubscribersDesc)
	if !slices.Equal(all, []uuid.UUID{confirmed, tech, food}) {
		t.Fatalf("visible %v", all)
	}
	if _, err := st.Catalog().Get(context.Background(), noPlatform, fee, rkn); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("channel without ord platform in card: %v", err)
	}

	from, to := money.Kopecks(115000), money.Kopecks(345000)
	for name, tc := range map[string]struct {
		f    domain.CatalogFilter
		want []uuid.UUID
	}{
		"q title":           {domain.CatalogFilter{Query: "рецепты"}, []uuid.UUID{food}},
		"q description":     {domain.CatalogFilter{Query: "СУПЫ"}, []uuid.UUID{food}},
		"q percent literal": {domain.CatalogFilter{Query: "100%"}, []uuid.UUID{food}},
		"q percent only":    {domain.CatalogFilter{Query: "%"}, []uuid.UUID{food}},
		"q underscore":      {domain.CatalogFilter{Query: "_"}, []uuid.UUID{tech}},
		"category":          {domain.CatalogFilter{CategoryID: &categoryTech}, []uuid.UUID{tech}},
		"format":            {domain.CatalogFilter{FormatID: "f_2_48"}, []uuid.UUID{tech, food}},
		// Без формата — минимальная включённая цена с комиссией: 345 000, 115 000, 1 150 000.
		"price range":     {domain.CatalogFilter{PriceFrom: &from, PriceTo: &to}, []uuid.UUID{tech, food}},
		"price to format": {domain.CatalogFilter{FormatID: "f_2_48", PriceTo: &to}, []uuid.UUID{tech}},
		"subscribers":     {domain.CatalogFilter{SubscribersFrom: p(6000), SubscribersTo: p(20000)}, []uuid.UUID{confirmed, tech}},
		"combined":        {domain.CatalogFilter{CategoryID: &channeltest.CategoryFood, FormatID: "f_1_24", SubscribersTo: p(10000)}, []uuid.UUID{food}},
	} {
		if got := list(t, st, tc.f, domain.SortSubscribersDesc); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}

	item, err := st.Catalog().Get(context.Background(), food, fee, rkn)
	if err != nil || len(item.Prices) != 2 || item.Prices[0].FormatID != "f_1_24" || item.Prices[0].Total != 345000 ||
		item.Category.Slug != "food" || item.Metrics != nil {
		t.Fatalf("card %+v %v", item, err)
	}
	if _, err := st.Catalog().Get(context.Background(), ids.New(), fee, rkn); err != domain.ErrNotFound {
		t.Fatalf("unknown channel: %v", err)
	}
}

func TestCatalogSorts(t *testing.T) {
	st, d := newStore(t)
	// a: 1000 подписчиков, цена 115 000, 100 просмотров → CPV 1150.
	a := seed(t, d, spec{subs: 1000, rating: 90, prices: price(100000), avgViews: p(100), engagement: p(1000)})
	// b: 3000, цена 230 000, 1000 просмотров → CPV 230.
	b := seed(t, d, spec{subs: 3000, rating: 95, prices: price(200000), avgViews: p(1000), engagement: p(3333)})
	// c: без метрик — в конце сортировок по просмотрам, вовлечённости и CPV.
	c := seed(t, d, spec{subs: 2000, rating: 100, prices: price(50000)})
	for sort, want := range map[domain.CatalogSort][]uuid.UUID{
		domain.SortSubscribersDesc: {b, c, a},
		domain.SortPriceAsc:        {c, a, b},
		domain.SortPriceDesc:       {b, a, c},
		domain.SortEngagementDesc:  {b, a, c},
		domain.SortRatingDesc:      {c, b, a},
		domain.SortAvgViewsDesc:    {b, a, c},
		domain.SortCPVAsc:          {b, a, c},
	} {
		if got := list(t, st, domain.CatalogFilter{}, sort); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", sort, got, want)
		}
	}
	items, err := st.Catalog().List(context.Background(), domain.CatalogQuery{Sort: domain.SortCPVAsc, Limit: 10, FeeRateBP: fee, RknThreshold: rkn})
	if err != nil || items[0].CPV == nil || *items[0].CPV != 230 || *items[1].CPV != 1150 || items[2].CPV != nil {
		t.Fatalf("cpv values: %+v %v", items, err)
	}
}

func TestCatalogCursorVisitsEachOnce(t *testing.T) {
	st, d := newStore(t)
	want := map[uuid.UUID]bool{}
	for i := range 11 {
		s := spec{subs: 500 * (i % 3), prices: price(100000 + int64(i%2)*10000)}
		if i%4 != 0 {
			s.avgViews = p(100 * (i % 2)) // равные значения и пустые ключи
		}
		want[seed(t, d, s)] = true
	}
	for _, sort := range domain.CatalogSorts {
		seen := map[uuid.UUID]bool{}
		var after *domain.CatalogCursor
		for page := 0; ; page++ {
			if page > 20 {
				t.Fatalf("%s: cursor does not advance", sort)
			}
			items, err := st.Catalog().List(context.Background(), domain.CatalogQuery{Sort: sort, After: after, Limit: 3, FeeRateBP: fee, RknThreshold: rkn})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				if seen[it.ID] {
					t.Fatalf("%s: %s twice", sort, it.ID)
				}
				seen[it.ID] = true
			}
			if len(items) < 3 {
				break
			}
			last := items[len(items)-1]
			after = &domain.CatalogCursor{Sort: sort, Value: last.SortValue, ID: last.ID}
		}
		if len(seen) != len(want) {
			t.Errorf("%s: visited %d of %d", sort, len(seen), len(want))
		}
	}
}

// TestCatalogPerformance — 07 §7: каталог с фильтрами на 2 000 каналах быстрее 1 с.
func TestCatalogPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	st, d := newStore(t)
	ctx := context.Background()
	org := channeltest.Organization(t, d.Pool)
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for i, q := range []string{
		`INSERT INTO channel (id, organization_id, max_chat_id, title, description, is_public, added_by_max_user_id,
			category_id, status, bot_is_admin, bot_permissions, subscribers_count, ord_platform_ready_at, created_at, updated_at)
		SELECT gen_random_uuid(), $1, -72000000000 - g, 'Канал ' || g, 'Описание ' || g, true, 7,
			(SELECT id FROM channel_category ORDER BY position OFFSET g % 8 LIMIT 1), 'active', true, ARRAY['write','delete'],
			(g * 37) % 9999, now(), now(), now()
		FROM generate_series(1, 2000) g`,
		`INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
		SELECT gen_random_uuid(), c.id, f.id, 10000 + (abs(hashtext(c.id::text || f.id)) % 1000000), true, now(), now()
		FROM channel c CROSS JOIN placement_format f`,
		`INSERT INTO channel_metrics (id, channel_id, date, subscribers_count, avg_views, engagement_bp, anomaly_flags, created_at, updated_at)
		SELECT gen_random_uuid(), c.id, date '2026-10-16' - d, c.subscribers_count, 1 + (abs(hashtext(c.id::text)) % 5000), 100, '{}', now(), now()
		FROM channel c CROSS JOIN generate_series(0, 29) d`,
	} {
		var args []any
		if i == 0 {
			args = []any{org} // первый запрос — каналы организации
		}
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	to := money.Kopecks(800000)
	started := time.Now()
	items, err := st.Catalog().List(ctx, domain.CatalogQuery{
		Filter: domain.CatalogFilter{FormatID: "f_2_48", PriceTo: &to, SubscribersFrom: p(100), Query: "канал"},
		Sort:   domain.SortCPVAsc, Limit: 21, FeeRateBP: fee, RknThreshold: rkn,
	})
	elapsed := time.Since(started)
	if err != nil || len(items) == 0 {
		t.Fatalf("items %d err %v", len(items), err)
	}
	t.Logf("catalog query on 2000 channels: %v", elapsed)
	if elapsed > time.Second {
		t.Fatalf("catalog query took %v, want < 1s", elapsed)
	}
}
