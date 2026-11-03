package httpapi

import (
	"time"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Время в ответах — UTC, время суток — ЧЧ:ММ по Москве (соглашения спецификации).

func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

func moneyPtr(k *money.Kopecks) *apigen.Money {
	if k == nil {
		return nil
	}
	v := apigen.Money(*k)
	return &v
}

func channelRef(c domain.ChannelRef) apigen.ChannelRef {
	return apigen.ChannelRef{Id: c.ID, Title: c.Title, IconUrl: opt(c.IconURL), Link: opt(c.Link)}
}

func toCart(v app.CartView) apigen.Cart {
	items := make([]apigen.CartItem, 0, len(v.Lines))
	for _, l := range v.Lines {
		items = append(items, apigen.CartItem{
			Id: l.Item.ID, Channel: channelRef(l.Channel), FormatId: l.Item.FormatID,
			ShownTotalAmount: int64(l.Item.ShownTotal), CurrentTotalAmount: moneyPtr(l.Current),
			PriceChanged: l.PriceChanged, Available: l.Available,
		})
	}
	return apigen.Cart{Items: items, TotalAmount: int64(v.Total)}
}

func counts(m map[domain.Status]int) (map[string]int, int) {
	out := make(map[string]int, len(m))
	total := 0
	for s, n := range m {
		out[string(s)] = n
		total += n
	}
	return out, total
}

func toOrderSummary(s app.OrderSummary) apigen.OrderSummary {
	by, total := counts(s.Counts)
	o := s.Order
	return apigen.OrderSummary{
		Id: o.ID, Number: o.Number, CampaignId: o.CampaignID, CampaignName: s.CampaignName, Status: apigen.OrderStatus(o.Status),
		TotalAmount: int64(o.TotalAmount), PlacementsCount: total, PlacementsByStatus: by, CreatedAt: utc(o.CreatedAt),
	}
}

func toOrder(v app.OrderView) apigen.Order {
	s := toOrderSummary(v.OrderSummary)
	placements := make([]apigen.AdvertiserPlacement, 0, len(v.Placements))
	for _, p := range v.Placements {
		placements = append(placements, toAdvertiserPlacement(p))
	}
	return apigen.Order{
		Id: s.Id, Number: s.Number, CampaignId: s.CampaignId, CampaignName: s.CampaignName, Status: s.Status,
		TotalAmount: s.TotalAmount, PlacementsCount: s.PlacementsCount, PlacementsByStatus: s.PlacementsByStatus,
		CreatedAt: s.CreatedAt, CurrentPostVersion: toPostVersion(v.Version), Placements: placements,
	}
}

func toPostVersion(v domain.VersionInfo) apigen.PostVersion {
	urls := v.MediaURLs
	if urls == nil {
		urls = []string{}
	}
	return apigen.PostVersion{
		Id: v.ID, Number: v.Number, Status: apigen.PostVersionStatus(v.Status), Body: opt(v.Body),
		FinalText: opt(v.FinalText), Erid: opt(v.Erid), MediaUrls: &urls,
		TokenCarrier: apigen.TokenCarrier(v.TokenCarrier), TokenPosition: apigen.TokenPosition(v.TokenPosition),
		RejectionReason: opt(v.RejectionReason), CreatedAt: utc(v.CreatedAt),
	}
}

func toHistory(list []domain.HistoryEntry) []apigen.PlacementHistoryEntry {
	out := make([]apigen.PlacementHistoryEntry, 0, len(list))
	for _, h := range list {
		e := apigen.PlacementHistoryEntry{
			Transition: string(h.Transition), ToStatus: apigen.PlacementStatus(h.To),
			ActorType: apigen.PlacementHistoryEntryActorType(h.ActorType), ReasonCode: opt(h.ReasonCode), CreatedAt: utc(h.CreatedAt),
		}
		if h.From != domain.StatusNone {
			from := apigen.PlacementStatus(h.From)
			e.FromStatus = &from
		}
		out = append(out, e)
	}
	return out
}

func toAdvertiserPlacement(v app.PlacementView) apigen.AdvertiserPlacement {
	p := v.Placement
	actions := make([]apigen.AdvertiserPlacementAction, 0, len(v.Actions))
	for _, a := range v.Actions {
		actions = append(actions, apigen.AdvertiserPlacementAction(a))
	}
	return apigen.AdvertiserPlacement{
		Id: p.ID, OrderId: p.OrderID, Channel: channelRef(v.Channel), Status: apigen.PlacementStatus(p.Status),
		StatusReason: opt(p.StatusReason), StatusChangedAt: utc(p.StatusChangedAt), TotalAmount: int64(p.TotalAmount),
		RefundAmount: moneyPtr(p.RefundAmount), PerformanceShare: p.PerformanceShare,
		PublicationUrl: publicationURL(v.Publication), Views: views(p, v.Publication), Dispute: disputeSummary(v.Dispute),
		Claim: claim(v.Claim), History: toHistory(v.History), AvailableActions: actions,
		FormatId: p.FormatID, TopHours: p.TopHours, DurationHours: p.DurationHours,
		WindowStart: utc(p.WindowStart), WindowEnd: utc(p.WindowEnd), TimeFrom: p.TimeFrom.String(), TimeTo: p.TimeTo.String(),
		AcceptDeadlineAt: utcPtr(p.AcceptDeadlineAt), ScheduleDeadlineAt: utcPtr(p.ScheduleDeadlineAt),
		PublishAt: utcPtr(p.PublishAt), RemoveAt: utcPtr(p.RemoveAt), PublishedAt: utcPtr(p.PublishedAt),
		RemovedAt: utcPtr(p.RemovedAt), ClaimDeadlineAt: utcPtr(p.ClaimDeadlineAt),
	}
}

func toBloggerPlacement(v app.BloggerView) apigen.BloggerPlacement {
	p := v.Placement
	actions := make([]apigen.BloggerPlacementAction, 0, len(v.Actions))
	for _, a := range v.Actions {
		actions = append(actions, apigen.BloggerPlacementAction(a))
	}
	out := apigen.BloggerPlacement{
		Id: p.ID, OrderNumber: v.OrderNumber, AdvertiserName: opt(v.AdvertiserName), Channel: channelRef(v.Channel),
		Status: apigen.PlacementStatus(p.Status), StatusReason: opt(p.StatusReason), StatusChangedAt: utc(p.StatusChangedAt),
		BloggerAmount: int64(p.BloggerAmount), PayoutAmount: moneyPtr(p.PayoutAmount),
		PublicationUrl: publicationURL(v.Publication), Views: views(p, v.Publication), Dispute: disputeSummary(v.Dispute),
		AvailableActions: actions,
		FormatId:         p.FormatID, TopHours: p.TopHours, DurationHours: p.DurationHours,
		WindowStart: utc(p.WindowStart), WindowEnd: utc(p.WindowEnd), TimeFrom: p.TimeFrom.String(), TimeTo: p.TimeTo.String(),
		AcceptDeadlineAt: utcPtr(p.AcceptDeadlineAt), ScheduleDeadlineAt: utcPtr(p.ScheduleDeadlineAt),
		PublishAt: utcPtr(p.PublishAt), RemoveAt: utcPtr(p.RemoveAt), PublishedAt: utcPtr(p.PublishedAt),
		RemovedAt: utcPtr(p.RemovedAt), ClaimDeadlineAt: utcPtr(p.ClaimDeadlineAt),
	}
	if v.Post != nil {
		urls := v.Post.MediaURLs
		if urls == nil {
			urls = []string{}
		}
		out.Post = &apigen.BloggerPost{FinalText: v.Post.FinalText, MediaUrls: urls, Erid: v.Post.Erid,
			ExtraRequirements: opt(v.Post.ExtraRequirements)}
	}
	return out
}

// publicationURL — публичная ссылка на пост в MAX; нет публикации или ссылки — поля нет.
func publicationURL(pub *domain.Publication) *string {
	if pub == nil || pub.URL == "" {
		return nil
	}
	return &pub.URL
}

// views — просмотры (R-18): после снятия — итоговые, иначе последнее считанное значение.
func views(p domain.Placement, pub *domain.Publication) *int {
	if p.FinalViews != nil {
		return p.FinalViews
	}
	if pub != nil {
		return pub.Views
	}
	return nil
}

// disputeSummary — сводка спора без предложенной доли: это материал администратора (R-18).
func disputeSummary(d *domain.Dispute) *apigen.PlacementDisputeSummary {
	if d == nil {
		return nil
	}
	out := &apigen.PlacementDisputeSummary{
		Reason: apigen.PlacementDisputeSummaryReason(d.Reason), OpenedByType: apigen.PlacementDisputeSummaryOpenedByType(d.OpenedBy),
		OpenedAt: utc(d.CreatedAt), DeadlineAt: utc(d.DeadlineAt), ResolvedAt: utcPtr(d.ResolvedAt),
	}
	if d.Resolved() {
		r := apigen.PlacementDisputeSummaryResolution(d.Resolution)
		out.Resolution = &r
	}
	return out
}

// claim — претензия рекламодателя в его кабинете (R-15 этапа приёмки).
func claim(c *domain.Claim) *apigen.PlacementClaim {
	if c == nil {
		return nil
	}
	return &apigen.PlacementClaim{Reason: apigen.ClaimReason(c.Reason), Text: c.Text, CreatedAt: utc(c.CreatedAt)}
}
