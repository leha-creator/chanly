package app

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Пределы страницы каталога (параметр limit спецификации).
const (
	catalogDefaultLimit = 20
	catalogMaxLimit     = 100
	// catalogHistoryDays — суточные снимки в карточке (S-02, R-19).
	catalogHistoryDays = 30
)

// CatalogRequest — запрос страницы каталога из кабинета рекламодателя.
type CatalogRequest struct {
	Filter domain.CatalogFilter
	Sort   domain.CatalogSort
	Cursor string
	Limit  int
}

// CatalogPage — страница каталога; NextCursor пуст на последней странице.
type CatalogPage struct {
	Items      []domain.CatalogItem
	NextCursor string
}

// ListCatalog — поиск в каталоге (S-02, R-7 … R-10). Значение поиска в журнал не пишется.
func (s *Service) ListCatalog(ctx context.Context, r CatalogRequest) (CatalogPage, error) {
	const op = "Catalog.List"
	if r.Sort == "" {
		r.Sort = domain.SortSubscribersDesc
	}
	if r.Limit <= 0 {
		r.Limit = catalogDefaultLimit
	}
	r.Limit = min(r.Limit, catalogMaxLimit)
	reject := func(err error) (CatalogPage, error) {
		var re *domain.RuleError
		if errors.As(err, &re) {
			s.log.WarnContext(ctx, "catalog query rejected", logx.KeyOp, op, "reason", re.Field+":"+re.Reason)
		}
		return CatalogPage{}, ruleError(err)
	}
	if !r.Sort.Valid() {
		return reject(&domain.RuleError{Field: "sort", Reason: "unknown"})
	}
	if err := r.Filter.Validate(); err != nil {
		return reject(err)
	}
	if r.Filter.FormatID != "" {
		formats, err := s.Deps.Formats.ListActive(ctx)
		if err != nil {
			return CatalogPage{}, err
		}
		if !slices.ContainsFunc(formats, func(f domain.Format) bool { return f.ID == r.Filter.FormatID }) {
			return reject(domain.ErrCatalogFormat)
		}
	}
	var after *domain.CatalogCursor
	if r.Cursor != "" {
		c, err := domain.DecodeCatalogCursor(r.Cursor, r.Sort)
		if err != nil {
			return reject(err)
		}
		after = c
	}
	items, err := s.Catalog.List(ctx, domain.CatalogQuery{
		Filter: r.Filter, Sort: r.Sort, After: after, Limit: r.Limit + 1,
		FeeRateBP: s.Config.FeeRateBP, RknThreshold: s.Config.RknThreshold,
	})
	if err != nil {
		return CatalogPage{}, err
	}
	page := CatalogPage{Items: items}
	if len(items) > r.Limit {
		page.Items = items[:r.Limit]
		last := page.Items[r.Limit-1]
		page.NextCursor = domain.CatalogCursor{Sort: r.Sort, Value: last.SortValue, ID: last.ID}.Encode()
	}
	s.log.DebugContext(ctx, "catalog listed", logx.KeyOp, op, "sort", r.Sort, "count", len(page.Items), "has_next", page.NextCursor != "")
	return page, nil
}

// CatalogChannel — карточка канала (S-02): последние 30 снимков, недоступность блогера
// и занятые размещениями интервалы (R-8). Невидимый канал — not_found (R-7).
func (s *Service) CatalogChannel(ctx context.Context, id uuid.UUID) (*domain.CatalogCard, error) {
	const op = "Catalog.Get"
	item, err := s.Catalog.Get(ctx, id, s.Config.FeeRateBP, s.Config.RknThreshold)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.DebugContext(ctx, "catalog channel not visible", logx.KeyOp, op, KeyChannelID, id)
		return nil, apperr.NotFound("channel not found")
	}
	if err != nil {
		return nil, err
	}
	history, err := s.Metrics.Recent(ctx, id, catalogHistoryDays)
	if err != nil {
		return nil, err
	}
	now := s.Clock.Now()
	unavail, err := s.Unavail.ListLive(ctx, id, now)
	if err != nil {
		return nil, err
	}
	busy, err := s.Placements.BusyIntervals(ctx, id, now, now.Add(s.Config.Unavailability.Horizon))
	if err != nil {
		return nil, err
	}
	unavail = append(unavail, busy...)
	slices.SortFunc(unavail, func(a, b domain.Unavailability) int { return a.StartsAt.Compare(b.StartsAt) })
	s.log.DebugContext(ctx, "catalog channel read", logx.KeyOp, op, KeyChannelID, id, "history", len(history), "unavailability", len(unavail))
	return &domain.CatalogCard{CatalogItem: *item, History: history, Unavailability: unavail}, nil
}
