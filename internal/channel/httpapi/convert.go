package httpapi

import (
	"context"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// view — канал для кабинета блогера с живыми интервалами недоступности.
func (h *Handlers) view(ctx context.Context, c *domain.Channel) (apigen.BloggerChannel, error) {
	v := bloggerChannel(c, h.svc.FeeRateBP())
	list, err := h.svc.Unavailability(ctx, c.ID)
	if err != nil {
		return apigen.BloggerChannel{}, err
	}
	v.Unavailability = unavailability(list)
	return v, nil
}

// bloggerPrices — цены блогера и цена для рекламодателя с комиссией (06 §3).
func bloggerPrices(prices []domain.Price, feeRateBP int) []apigen.BloggerPrice {
	out := make([]apigen.BloggerPrice, 0, len(prices))
	for _, p := range prices {
		out = append(out, apigen.BloggerPrice{
			FormatId: p.FormatID, Amount: int64(p.Amount), IsEnabled: p.IsEnabled,
			TotalAmount: int64(domain.TotalAmount(p.Amount, feeRateBP)),
		})
	}
	return out
}

func unavailability(list []domain.Unavailability) []apigen.Unavailability {
	out := make([]apigen.Unavailability, 0, len(list))
	for _, u := range list {
		out = append(out, apigen.Unavailability{Id: u.ID, StartsAt: u.StartsAt, EndsAt: u.EndsAt})
	}
	return out
}

func catalogPrices(list []domain.CatalogPrice) []apigen.CatalogPrice {
	out := make([]apigen.CatalogPrice, 0, len(list))
	for _, p := range list {
		out = append(out, apigen.CatalogPrice{FormatId: p.FormatID, TotalAmount: int64(p.Total)})
	}
	return out
}

func metricsHistory(list []domain.MetricsSnapshot) []apigen.ChannelMetricsPoint {
	out := make([]apigen.ChannelMetricsPoint, 0, len(list))
	for _, m := range list {
		out = append(out, apigen.ChannelMetricsPoint{
			Date: openapi_types.Date{Time: m.Date}, SubscribersCount: m.SubscribersCount, AvgViews: m.AvgViews,
		})
	}
	return out
}

// flags переводит флаги аномалий в тип схемы; пустой список — поля нет.
func flags[T ~string](list []string) *[]T {
	if len(list) == 0 {
		return nil
	}
	out := make([]T, 0, len(list))
	for _, f := range list {
		out = append(out, T(f))
	}
	return &out
}

// catalogChannel — строка каталога; inCart — канал уже в корзине организации (R-8 этапа
// «Заказ и размещения»).
func catalogChannel(it domain.CatalogItem, inCart bool) apigen.CatalogChannel {
	out := apigen.CatalogChannel{
		Id: it.ID, Title: it.Title, IconUrl: opt(it.IconURL), Link: it.Link, Description: opt(it.Description),
		Category:         apigen.ChannelCategory{Id: it.Category.ID, Slug: it.Category.Slug, Name: it.Category.Name},
		SubscribersCount: it.SubscribersCount, Rating: it.Rating, RknNumber: opt(it.RknNumber),
		Prices: catalogPrices(it.Prices), Cpv: kopecks(it.CPV), InCart: &inCart,
	}
	if m := it.Metrics; m != nil {
		out.AvgViews, out.EngagementBp = m.AvgViews, m.EngagementBP
		out.AnomalyFlags = flags[apigen.CatalogChannelAnomalyFlags](m.AnomalyFlags)
	}
	return out
}

func catalogDetail(card *domain.CatalogCard, inCart bool) apigen.CatalogChannelDetail {
	b := catalogChannel(card.CatalogItem, inCart)
	out := apigen.CatalogChannelDetail{
		Id: b.Id, Title: b.Title, IconUrl: b.IconUrl, Link: b.Link, Description: b.Description, Category: b.Category,
		SubscribersCount: b.SubscribersCount, AvgViews: b.AvgViews, EngagementBp: b.EngagementBp, Rating: b.Rating,
		RknNumber: b.RknNumber, Prices: b.Prices, Cpv: b.Cpv, InCart: b.InCart, MaxDescription: opt(card.MaxDescription),
		MetricsHistory: metricsHistory(card.History), Unavailability: unavailability(card.Unavailability),
	}
	if m := card.Metrics; m != nil {
		out.PostsPerWeek = m.PostsPerWeek
		out.AnomalyFlags = flags[apigen.CatalogChannelDetailAnomalyFlags](m.AnomalyFlags)
	}
	return out
}

func kopecks(k *money.Kopecks) *int64 {
	if k == nil {
		return nil
	}
	v := int64(*k)
	return &v
}
