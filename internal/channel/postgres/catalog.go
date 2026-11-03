package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Catalog — выборки каталога рекламодателя. Запрос пишется руками на pgx, а не sqlc
// (R-11): переменный ORDER BY и необязательные фильтры с ключевым курсором. Условия
// добавляются из закрытого списка, сортировка — из таблицы sortExprs; все значения
// передаются параметрами.
type Catalog struct{ s *Store }

// sortSpec — выражение ключа сортировки над строкой base и направление.
type sortSpec struct {
	expr string
	desc bool
}

// sortExprs — единственные выражения, которые попадают в ORDER BY. Ключ — bigint;
// каналы без значения идут в конце, при равенстве — по id (R-9).
var sortExprs = map[domain.CatalogSort]sortSpec{
	domain.SortSubscribersDesc: {"b.subscribers_count::bigint", true},
	domain.SortPriceAsc:        {"b.sort_price", false},
	domain.SortPriceDesc:       {"b.sort_price", true},
	domain.SortEngagementDesc:  {"b.engagement_bp::bigint", true},
	domain.SortRatingDesc:      {"b.rating::bigint", true},
	domain.SortAvgViewsDesc:    {"b.avg_views::bigint", true},
	domain.SortCPVAsc:          {"CASE WHEN b.avg_views > 0 THEN b.sort_price / b.avg_views END", false},
}

// args — накопитель параметров запроса: Add возвращает плейсхолдер $n.
type args []any

func (a *args) Add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// baseQuery — видимые каналы (R-7) с ценой для фильтра и сортировки и последним
// снимком метрик. Цена для рекламодателя — amount + floor(amount × fee / 10000),
// та же формула, что domain.TotalAmount. Без format_id — минимальная включённая цена.
func baseQuery(a *args, f domain.CatalogFilter, feeRateBP, rknThreshold int, id *uuid.UUID) string {
	fee := a.Add(int64(feeRateBP))
	priceCond := ""
	if f.FormatID != "" {
		priceCond = " AND p.format_id = " + a.Add(f.FormatID)
	}
	where := []string{
		"c.status = 'active'",
		// R-14 этапа «Маркировка»: площадка канала зарегистрирована в ОРД.
		"c.ord_platform_ready_at IS NOT NULL",
		"(c.subscribers_count < " + a.Add(int32(rknThreshold)) + " OR c.rkn_status = 'confirmed')",
	}
	if id != nil {
		where = append(where, "c.id = "+a.Add(*id))
	}
	if f.Query != "" {
		q := a.Add("%" + escapeLike(f.Query) + "%")
		where = append(where, "(c.title ILIKE "+q+" OR c.description ILIKE "+q+")")
	}
	if f.CategoryID != nil {
		where = append(where, "c.category_id = "+a.Add(*f.CategoryID))
	}
	if f.SubscribersFrom != nil {
		where = append(where, "c.subscribers_count >= "+a.Add(int32(*f.SubscribersFrom)))
	}
	if f.SubscribersTo != nil {
		where = append(where, "c.subscribers_count <= "+a.Add(int32(*f.SubscribersTo)))
	}
	if f.PriceFrom != nil {
		where = append(where, "pr.price >= "+a.Add(int64(*f.PriceFrom)))
	}
	if f.PriceTo != nil {
		where = append(where, "pr.price <= "+a.Add(int64(*f.PriceTo)))
	}
	return `SELECT c.id, coalesce(c.title, '') AS title, coalesce(c.icon_url, '') AS icon_url, coalesce(c.link, '') AS link,
		coalesce(c.description, '') AS description, coalesce(c.max_description, '') AS max_description,
		c.subscribers_count, c.rating, coalesce(c.rkn_number, '') AS rkn_number,
		cat.id AS category_id, cat.slug AS category_slug, cat.name AS category_name, cat.position AS category_position,
		m.date AS metrics_date, m.subscribers_count AS metrics_subscribers, m.avg_views, m.engagement_bp,
		m.posts_per_week, m.anomaly_flags, pr.price AS sort_price
	FROM channel c
	JOIN channel_category cat ON cat.id = c.category_id
	JOIN LATERAL (
		SELECT min(p.amount + p.amount * ` + fee + ` / 10000) AS price
		FROM channel_price p JOIN placement_format f ON f.id = p.format_id
		WHERE p.channel_id = c.id AND p.is_enabled AND f.is_active` + priceCond + `
	) pr ON pr.price IS NOT NULL
	LEFT JOIN LATERAL (
		SELECT date, subscribers_count, avg_views, engagement_bp, posts_per_week, anomaly_flags
		FROM channel_metrics WHERE channel_id = c.id ORDER BY date DESC LIMIT 1
	) m ON true
	WHERE ` + strings.Join(where, " AND ")
}

// escapeLike экранирует служебные знаки ILIKE: поиск — буквальный (R-11).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *Catalog) List(ctx context.Context, q domain.CatalogQuery) ([]domain.CatalogItem, error) {
	const op = "Channel.Catalog.List"
	// Длительность запроса для журнала (07 §7: до 1 с) — не предметное время, поэтому не clock.
	started := time.Now()
	spec, ok := sortExprs[q.Sort]
	if !ok {
		return nil, fmt.Errorf("catalog: unknown sort %q", q.Sort)
	}
	var a args
	base := baseQuery(&a, q.Filter, q.FeeRateBP, q.RknThreshold, nil)
	dir, cmp := "ASC", ">"
	if spec.desc {
		dir, cmp = "DESC", "<"
	}
	cond := "true"
	if q.After != nil {
		id := a.Add(q.After.ID)
		if q.After.Value == nil {
			cond = "s.sort_value IS NULL AND s.id > " + id
		} else {
			v := a.Add(*q.After.Value)
			cond = "(s.sort_value " + cmp + " " + v + " OR (s.sort_value = " + v + " AND s.id > " + id + ") OR s.sort_value IS NULL)"
		}
	}
	sql := `SELECT * FROM (SELECT b.*, (` + spec.expr + `)::bigint AS sort_value FROM (` + base + `) b) s
		WHERE ` + cond + `
		ORDER BY s.sort_value ` + dir + ` NULLS LAST, s.id
		LIMIT ` + a.Add(int32(q.Limit))
	items, err := r.query(ctx, op, sql, a, true)
	if err != nil {
		return nil, err
	}
	if err := r.attachPrices(ctx, items, q.FeeRateBP); err != nil {
		return nil, err
	}
	r.s.log.DebugContext(ctx, "catalog queried", "op", op, "sort", q.Sort, "filters", filterNames(q.Filter),
		"count", len(items), "elapsed_ms", time.Since(started).Milliseconds())
	return items, nil
}

func (r *Catalog) Get(ctx context.Context, id uuid.UUID, feeRateBP, rknThreshold int) (*domain.CatalogItem, error) {
	const op = "Channel.Catalog.Get"
	var a args
	items, err := r.query(ctx, op, baseQuery(&a, domain.CatalogFilter{}, feeRateBP, rknThreshold, &id), a, false)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		r.s.debug(ctx, op, "channel_id", id, "result", "not visible")
		return nil, domain.ErrNotFound
	}
	if err := r.attachPrices(ctx, items, feeRateBP); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func (r *Catalog) query(ctx context.Context, op, sql string, a args, withSort bool) ([]domain.CatalogItem, error) {
	rows, err := db.Conn(ctx, r.s.pool).Query(ctx, sql, a...)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	defer rows.Close()
	var out []domain.CatalogItem
	for rows.Next() {
		var (
			it                    domain.CatalogItem
			subs, rating, catPos  int32
			metricsDate           pgtype.Date
			metricsSubs, avg, eng *int32
			ppw                   pgtype.Numeric
			flags                 []string
			sortPrice             int64
			sortValue             *int64
		)
		dest := []any{&it.ID, &it.Title, &it.IconURL, &it.Link, &it.Description, &it.MaxDescription,
			&subs, &rating, &it.RknNumber, &it.Category.ID, &it.Category.Slug, &it.Category.Name, &catPos,
			&metricsDate, &metricsSubs, &avg, &eng, &ppw, &flags, &sortPrice}
		if withSort {
			dest = append(dest, &sortValue)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, r.s.fail(ctx, op, err)
		}
		it.SubscribersCount, it.Rating, it.Category.Position = int(subs), int(rating), int(catPos)
		if metricsDate.Valid {
			m := snapshot(metricsDate, *metricsSubs, avg, eng, ppw, flags)
			it.Metrics = &m
		}
		it.SortValue = sortValue
		var avgViews *int
		if it.Metrics != nil {
			avgViews = it.Metrics.AvgViews
		}
		it.CPV = domain.CPV(money.Kopecks(sortPrice), avgViews)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, r.s.fail(ctx, op, err)
	}
	return out, nil
}

// attachPrices — включённые цены активных форматов с комиссией.
func (r *Catalog) attachPrices(ctx context.Context, items []domain.CatalogItem, feeRateBP int) error {
	list := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		list = append(list, it.ID)
	}
	prices, err := r.s.pricesByChannel(ctx, list)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Prices = []domain.CatalogPrice{}
		for _, p := range prices[items[i].ID] {
			if p.Offered() {
				items[i].Prices = append(items[i].Prices, domain.CatalogPrice{FormatID: p.FormatID, Total: domain.TotalAmount(p.Amount, feeRateBP)})
			}
		}
	}
	return nil
}

// filterNames — имена заданных фильтров для журнала; значение поиска не пишется.
func filterNames(f domain.CatalogFilter) []string {
	out := []string{}
	for name, set := range map[string]bool{
		"q": f.Query != "", "category_id": f.CategoryID != nil, "format_id": f.FormatID != "",
		"price_from": f.PriceFrom != nil, "price_to": f.PriceTo != nil,
		"subscribers_from": f.SubscribersFrom != nil, "subscribers_to": f.SubscribersTo != nil,
	} {
		if set {
			out = append(out, name)
		}
	}
	return out
}
