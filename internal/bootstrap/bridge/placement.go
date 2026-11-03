// Package bridge — переходники портов между модулями (ARCHITECTURE.md, «Порт объявляет
// потребитель»): порт одного модуля поверх сервиса другого, когда сигнатуры не совпадают.
// Пакет — часть сборки: его используют bootstrap и сценарные тесты модулей.
package bridge

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// ---------------------------------------------------------------- placement ← channel

// PlacementChannelRatings — placement/domain.ChannelRatings поверх сервиса модуля каналов (санкция
// по решению спора, A-08).
type PlacementChannelRatings struct{ Svc *channelapp.Service }

var _ domain.ChannelRatings = PlacementChannelRatings{}

func (b PlacementChannelRatings) Lower(ctx context.Context, channelID uuid.UUID, points int) (int, int, error) {
	from, to, err := b.Svc.LowerRating(ctx, channelID, points)
	if channelapp.IsNotFound(err) {
		return 0, 0, domain.ErrNotFound
	}
	return from, to, err
}

// PlacementChannels — placement/domain.Channels поверх сервиса модуля каналов.
type PlacementChannels struct{ Svc *channelapp.Service }

var _ domain.Channels = PlacementChannels{}

func (b PlacementChannels) Offers(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.ChannelOffer, error) {
	list, err := b.Svc.ChannelsForPlacement(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.ChannelOffer, len(list))
	for id, pc := range list {
		out[id] = offer(pc)
	}
	return out, nil
}

func (b PlacementChannels) LockForPlacement(ctx context.Context, id uuid.UUID) (domain.ChannelOffer, error) {
	pc, err := b.Svc.LockChannelForPlacement(ctx, id)
	if channelapp.IsNotFound(err) {
		return domain.ChannelOffer{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ChannelOffer{}, err
	}
	return offer(pc), nil
}

func (b PlacementChannels) Unavailability(ctx context.Context, id uuid.UUID, from, to time.Time) ([]domain.Interval, error) {
	list, err := b.Svc.UnavailabilityBetween(ctx, id, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Interval, 0, len(list))
	for _, u := range list {
		out = append(out, domain.Interval{Start: u.StartsAt, End: u.EndsAt})
	}
	return out, nil
}

func offer(pc channelapp.PlacementChannel) domain.ChannelOffer {
	c := pc.Channel
	prices := make([]domain.FormatPrice, 0, len(c.Prices))
	for _, p := range c.Prices {
		f, active := pc.Formats[p.FormatID]
		prices = append(prices, domain.FormatPrice{
			Format: domain.Format{ID: p.FormatID, TopHours: f.TopHours, DurationHours: f.DurationHours},
			Amount: p.Amount, Enabled: p.IsEnabled, FormatActive: p.FormatActive && active,
		})
	}
	return domain.ChannelOffer{
		Ref:       domain.ChannelRef{ID: c.ID, Title: c.Title, IconURL: c.IconURL, Link: c.Link},
		MaxChatID: c.MaxChatID, OwnerOrgID: c.OrganizationID, Active: c.Status == channeldomain.StatusActive, Visible: pc.Visible,
		Prices: prices,
	}
}

// ---------------------------------------------------------------- placement ← post

// PlacementCampaigns — placement/domain.Campaigns поверх сервиса модуля post (R-1).
type PlacementCampaigns struct{ Svc *postapp.Service }

var _ domain.Campaigns = PlacementCampaigns{}

func (b PlacementCampaigns) LockForCheckout(ctx context.Context, orgID, campaignID uuid.UUID) (domain.CampaignSnapshot, error) {
	c, err := b.Svc.LockCampaignForCheckout(ctx, orgID, campaignID)
	if err != nil {
		return domain.CampaignSnapshot{}, err
	}
	snap := snapshot(c.Campaign)
	snap.PostID = c.PostID
	return snap, nil
}

func (b PlacementCampaigns) Get(ctx context.Context, orgID, campaignID uuid.UUID) (domain.CampaignSnapshot, error) {
	c, err := b.Svc.GetCampaign(ctx, orgID, campaignID)
	if err != nil {
		return domain.CampaignSnapshot{}, err
	}
	return snapshot(c), nil
}

func (b PlacementCampaigns) MarkOrdered(ctx context.Context, orgID, campaignID, orderID uuid.UUID) error {
	return b.Svc.MarkCampaignOrdered(ctx, orgID, campaignID, orderID)
}

func (b PlacementCampaigns) Archive(ctx context.Context, orgID, campaignID uuid.UUID) error {
	return b.Svc.ArchiveCampaign(ctx, orgID, campaignID)
}

func snapshot(c postdomain.Campaign) domain.CampaignSnapshot {
	s := domain.CampaignSnapshot{
		ID: c.ID, OrganizationID: c.OrganizationID, Name: c.Name, Status: domain.CampaignStatus(c.Status),
		DateFrom: c.DateFrom, DateTo: c.DateTo, OrderID: c.OrderID,
	}
	if c.TimeFrom != nil {
		v := domain.TimeOfDay(*c.TimeFrom)
		s.TimeFrom = &v
	}
	if c.TimeTo != nil {
		v := domain.TimeOfDay(*c.TimeTo)
		s.TimeTo = &v
	}
	if c.ExtraRequirements != nil {
		s.ExtraRequirements = *c.ExtraRequirements
	}
	return s
}

// PlacementVersions — placement/domain.PostVersions поверх сервиса модуля post.
type PlacementVersions struct{ Svc *postapp.Service }

var _ domain.PostVersions = PlacementVersions{}

// problemsDetail и reasonPostNotReady — details отказа post/app.CreateVersion.
const (
	problemsDetail     = "problems"
	reasonPostNotReady = "post_not_ready"
)

func (b PlacementVersions) Create(ctx context.Context, orgID, campaignID, userID uuid.UUID, orderNumber int64) (domain.VersionInfo, error) {
	v, err := b.Svc.CreateVersion(ctx, orgID, campaignID, userID, orderNumber)
	if e, ok := apperr.As(err); ok && e.Code == apperr.CodeConflict && e.Details["reason"] == reasonPostNotReady {
		problems, _ := e.Details[problemsDetail].([]string)
		return domain.VersionInfo{}, &domain.PostNotReadyError{Problems: problems}
	}
	if err != nil {
		return domain.VersionInfo{}, err
	}
	return versionInfo(postapp.VersionView{Version: v}), nil
}

func (b PlacementVersions) Get(ctx context.Context, versionID uuid.UUID) (domain.VersionInfo, error) {
	v, err := b.Svc.GetVersion(ctx, versionID)
	if err != nil {
		return domain.VersionInfo{}, err
	}
	return versionInfo(v), nil
}

func versionInfo(v postapp.VersionView) domain.VersionInfo {
	urls := v.MediaURLs
	if urls == nil {
		urls = []string{}
	}
	return domain.VersionInfo{
		ID: v.Version.ID, Number: v.Version.Number, Status: domain.VersionStatus(v.Version.Status), Body: v.Version.Body,
		FinalText: v.Version.FinalText, AdvertiserName: v.Version.AdvertiserName, Erid: v.Version.Erid, MediaURLs: urls,
		TokenCarrier: string(v.Version.TokenCarrier), TokenPosition: string(v.Version.TokenPosition),
		RejectionReason: v.Version.RejectionReason, CreatedAt: v.Version.CreatedAt,
		Links: postdomain.BodyLinkURLs(v.Version.Body), UTMSource: postdomain.UTMSource, UTMMedium: postdomain.UTMMedium,
	}
}

// ---------------------------------------------------------------- post → placement

// VersionListener — post/domain.VersionListener поверх сервиса размещений (R-5).
type VersionListener struct{ Svc *placementapp.Service }

var _ postdomain.VersionListener = VersionListener{}

func (b VersionListener) VersionChanged(ctx context.Context, c postdomain.VersionChange) error {
	return b.Svc.VersionChanged(ctx, c.VersionID, domain.VersionStatus(c.To))
}

// ---------------------------------------------------------------- channel → placement

// ChannelPlacements — порты модуля каналов поверх сервиса размещений: последствия потери
// канала (S-25), занятые интервалы карточки (R-8 этапа «Каталог»), признак in_cart.
type ChannelPlacements struct{ Svc *placementapp.Service }

var (
	_ channeldomain.AvailabilityListener = ChannelPlacements{}
	_ channeldomain.PlacementSchedule    = ChannelPlacements{}
	_ channeldomain.CartLookup           = ChannelPlacements{}
)

func (b ChannelPlacements) ChannelUnavailable(ctx context.Context, c channeldomain.StatusChange) error {
	return b.Svc.ChannelUnavailable(ctx, c.ChannelID, string(c.From), string(c.To))
}

func (b ChannelPlacements) ChannelRestored(ctx context.Context, c channeldomain.StatusChange) error {
	return b.Svc.ChannelRestored(ctx, c.ChannelID, string(c.From), string(c.To))
}

func (b ChannelPlacements) BusyIntervals(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]channeldomain.Unavailability, error) {
	list, err := b.Svc.BusyIntervals(ctx, channelID, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]channeldomain.Unavailability, 0, len(list))
	for _, iv := range list {
		out = append(out, channeldomain.Unavailability{StartsAt: iv.Start, EndsAt: iv.End})
	}
	return out, nil
}

func (b ChannelPlacements) ScheduledConflict(ctx context.Context, channelID uuid.UUID, from, to time.Time) (bool, error) {
	return b.Svc.ScheduledConflict(ctx, channelID, from, to)
}

func (b ChannelPlacements) InCart(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	return b.Svc.InCart(ctx, orgID, ids)
}

// ---------------------------------------------------------------- будущие модули (R-3, R-4)

// AllowAllContractors — проверка исполнителя до подключения модуля выплат (SetPayoutPorts, R-3, A-36).
type AllowAllContractors struct{ Log *slog.Logger }

var _ domain.ContractorGate = AllowAllContractors{}

func (g AllowAllContractors) CanAccept(ctx context.Context, bloggerOrgID uuid.UUID) (bool, error) {
	g.Log.DebugContext(ctx, "contractor check skipped, no payouts yet", logx.KeyOp, "Placement.Contractors",
		logx.KeyOrganizationID, bloggerOrgID)
	return true, nil
}

// PlacementPostModeration — решение администратора по версии поста (S-17) для модуля размещений:
// модуль post. MarkingOff — ORD_ENABLED=false (I-2 этапа «API администратора»).
type PlacementPostModeration struct {
	Svc        *postapp.Service
	MarkingOff bool
}

func (b PlacementPostModeration) Approve(ctx context.Context, versionID, adminID uuid.UUID) error {
	_, err := b.Svc.ApproveVersion(ctx, versionID, adminID)
	return err
}

func (b PlacementPostModeration) Reject(ctx context.Context, versionID, adminID uuid.UUID, reason string) error {
	_, err := b.Svc.RejectVersion(ctx, versionID, adminID, reason)
	return err
}

func (b PlacementPostModeration) MarkingDisabled() bool { return b.MarkingOff }

func (b PlacementPostModeration) MarkReady(ctx context.Context, versionID uuid.UUID, erid string) error {
	_, err := b.Svc.MarkVersionReady(ctx, versionID, erid)
	return err
}
