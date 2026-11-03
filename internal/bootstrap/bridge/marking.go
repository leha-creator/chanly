package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/integration/ord"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// ---------------------------------------------------------------- marking ← account

// MarkingOrganizations — marking/domain.Organizations поверх хранилища модуля аккаунтов
// (чтение без блокировки).
type MarkingOrganizations struct{ Orgs *accountpg.Organizations }

var _ domain.Organizations = MarkingOrganizations{}

func (b MarkingOrganizations) Details(ctx context.Context, orgID uuid.UUID) (domain.OrgDetails, error) {
	d, err := b.Orgs.ForMarking(ctx, orgID)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return domain.OrgDetails{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OrgDetails{}, err
	}
	return domain.OrgDetails{ID: d.ID, LegalType: d.LegalType, INN: d.INN, Name: d.Name, KPP: d.KPP, IsPlatform: d.IsPlatform}, nil
}

func (b MarkingOrganizations) Platform(ctx context.Context) (domain.OrgDetails, error) {
	p, err := b.Orgs.Platform(ctx)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return domain.OrgDetails{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OrgDetails{}, err
	}
	return b.Details(ctx, p.ID)
}

func (b MarkingOrganizations) FirstOfferAcceptance(ctx context.Context, orgID uuid.UUID, kind string) (domain.OfferAcceptance, bool, error) {
	at, version, ok, err := b.Orgs.FirstOfferAcceptance(ctx, orgID, accountdomain.LegalDocumentKind(kind))
	if err != nil || !ok {
		return domain.OfferAcceptance{}, false, err
	}
	return domain.OfferAcceptance{AcceptedAt: at, Version: version}, true, nil
}

// ---------------------------------------------------------------- marking ← post

// MarkingPosts — содержание версии для креатива и перевод версии в ready (R-8) поверх
// сервиса модуля post.
type MarkingPosts struct{ Svc *postapp.Service }

var (
	_ domain.CreativeSource = MarkingPosts{}
	_ domain.VersionMarker  = MarkingPosts{}
)

func (b MarkingPosts) CreativeContent(ctx context.Context, versionID uuid.UUID) (domain.CreativeContent, error) {
	c, err := b.Svc.CreativeContent(ctx, versionID)
	if apperr.Is(err, apperr.CodeNotFound) {
		return domain.CreativeContent{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.CreativeContent{}, err
	}
	return domain.CreativeContent{
		VersionID: c.VersionID, VersionStatus: string(c.Status), OrganizationID: c.OrganizationID,
		Text: c.Text, URLs: c.URLs, ImageURLs: c.ImageURLs,
	}, nil
}

func (b MarkingPosts) MarkVersionReady(ctx context.Context, versionID uuid.UUID, erid string) error {
	_, err := b.Svc.MarkVersionReady(ctx, versionID, erid)
	return err
}

// ---------------------------------------------------------------- marking ← integration/ord

// OrdClient — методы адаптера ОРД: integration/ord.Client или ordfake.Client.
type OrdClient interface {
	UpsertOrganization(ctx context.Context, o ord.Organization) (ord.Receipt, error)
	UpsertContract(ctx context.Context, c ord.Contract) (ord.Receipt, error)
	UpsertPlatforms(ctx context.Context, p ord.Platforms) (ord.Receipt, error)
	UpsertCreative(ctx context.Context, c ord.Creative) (ord.Receipt, error)
	UpsertStatistics(ctx context.Context, s ord.Statistics) (ord.Receipt, error)
	UpsertInvoice(ctx context.Context, inv ord.Invoice) (ord.Receipt, error)
	UpsertInvoiceCreatives(ctx context.Context, ic ord.InvoiceCreatives) (ord.Receipt, error)
	GetStatus(ctx context.Context, requestID string) (ord.Receipt, error)
}

// MarkingOrd — marking/domain.OrdAPI поверх адаптера ОРД: поля запросов переносятся один в один,
// поэтому хеш полезной нагрузки (R-6) совпадает с отправленным телом.
type MarkingOrd struct{ Client OrdClient }

var _ domain.OrdAPI = MarkingOrd{}

func (b MarkingOrd) UpsertOrganization(ctx context.Context, r domain.OrganizationRequest) (domain.Receipt, error) {
	rec, err := b.Client.UpsertOrganization(ctx, ord.Organization{
		ID: r.ID, Type: r.Type, IsOrs: r.IsOrs, IsRr: r.IsRr, INN: r.INN, Name: r.Name, KPP: r.KPP,
	})
	return receipt(rec), err
}

func (b MarkingOrd) UpsertContract(ctx context.Context, r domain.ContractRequest) (domain.Receipt, error) {
	rec, err := b.Client.UpsertContract(ctx, ord.Contract{
		ID: r.ID, Type: r.Type, ContractorID: r.ContractorID, ClientID: r.ClientID, IsRegReport: r.IsRegReport,
		Date: r.Date, Number: r.Number, SubjectType: r.SubjectType, ActionType: r.ActionType,
		AgentActingForPublisher: r.AgentActingForPublisher,
	})
	return receipt(rec), err
}

func (b MarkingOrd) UpsertPlatform(ctx context.Context, r domain.PlatformRequest) (domain.Receipt, error) {
	p := ord.Platforms{OrganizationID: r.OrganizationID}
	for _, it := range r.Platforms {
		p.Platforms = append(p.Platforms, ord.Platform{PlatformID: it.PlatformID, IsOwned: it.IsOwned, Type: it.Type, Name: it.Name, URL: it.URL})
	}
	rec, err := b.Client.UpsertPlatforms(ctx, p)
	return receipt(rec), err
}

func (b MarkingOrd) UpsertCreative(ctx context.Context, r domain.CreativeRequest) (domain.Receipt, error) {
	c := ord.Creative{
		ID: r.ID, CreativeType: r.CreativeType, IsSocial: r.IsSocial, IsSocialQuota: r.IsSocialQuota, CoBranding: r.CoBranding,
		ContractIDs: r.ContractIDs, KKTUCodes: r.KKTUCodes, Form: r.Form, TextData: r.TextData, URLs: r.URLs,
	}
	for _, m := range r.MediaData {
		c.MediaData = append(c.MediaData, ord.MediaData{MediaURL: m.MediaURL, MediaURLFileType: m.MediaURLFileType})
	}
	rec, err := b.Client.UpsertCreative(ctx, c)
	return receipt(rec), err
}

func (b MarkingOrd) UpsertStatistics(ctx context.Context, r domain.StatisticsRequest) (domain.Receipt, error) {
	rec, err := b.Client.UpsertStatistics(ctx, ord.Statistics{
		ID: r.ID, CreativeID: r.CreativeID, PlatformID: r.PlatformID, ImpsFact: r.ImpsFact, ImpsPlan: r.ImpsPlan,
		StartDateFact: r.StartDateFact, EndDateFact: r.EndDateFact, StartDatePlan: r.StartDatePlan, EndDatePlan: r.EndDatePlan,
		Amount: ord.Amount{Incl: r.Amount},
	})
	return receipt(rec), err
}

// UpsertInvoice — акт, затем разбивка (R-22): квитанция второго вызова, по ней идёт опрос.
// ПРЕДПОЛОЖЕНИЕ (Q-42): статус акта ОРД сообщает по reqid разбивки.
func (b MarkingOrd) UpsertInvoice(ctx context.Context, r domain.InvoiceRequest) (domain.Receipt, error) {
	if _, err := b.Client.UpsertInvoice(ctx, ord.Invoice{
		ID: r.ID, ContractID: r.ContractID, Type: r.Type, Number: r.Number, Date: r.Date, StartDate: r.StartDate,
		EndDate: r.EndDate, Amount: ord.Amount{Incl: r.Amount}, IsVat: r.IsVat,
	}); err != nil {
		return domain.Receipt{}, err
	}
	ic := ord.InvoiceCreatives{InvoiceID: r.ID, Items: make([]ord.InvoiceItem, 0, len(r.Items))}
	for _, it := range r.Items {
		ic.Items = append(ic.Items, ord.InvoiceItem{CreativeID: it.CreativeID, PlatformID: it.PlatformID, Amount: ord.Amount{Incl: it.Amount}})
	}
	rec, err := b.Client.UpsertInvoiceCreatives(ctx, ic)
	return receipt(rec), err
}

func (b MarkingOrd) GetStatus(ctx context.Context, requestID string) (domain.Receipt, error) {
	rec, err := b.Client.GetStatus(ctx, requestID)
	return receipt(rec), err
}

func receipt(r ord.Receipt) domain.Receipt {
	out := domain.Receipt{RequestID: r.RequestID, Status: r.Status, Token: r.Token, Errors: r.Errors()}
	for _, o := range r.ObjectStatuses {
		out.ObjectStatuses = append(out.ObjectStatuses, domain.ObjectStatus{
			ObjectID: o.ObjectID, Status: o.Status, Token: o.Token, Errors: o.Errors(),
		})
	}
	return out
}

// ---------------------------------------------------------------- post, channel → marking

// MarkingVersionListener — post/domain.VersionListener поверх сервиса маркировки (R-11):
// одобренная версия получает креатив в той же транзакции.
type MarkingVersionListener struct{ Svc *markingapp.Service }

var _ postdomain.VersionListener = MarkingVersionListener{}

func (b MarkingVersionListener) VersionChanged(ctx context.Context, c postdomain.VersionChange) error {
	return b.Svc.VersionChanged(ctx, c.VersionID, string(c.To))
}

// ChannelApprovals — channel/domain.ChannelApprovalListener поверх сервиса маркировки (R-13).
type ChannelApprovals struct{ Svc *markingapp.Service }

var _ channeldomain.ChannelApprovalListener = ChannelApprovals{}

func (b ChannelApprovals) ChannelApproved(ctx context.Context, ch channeldomain.ApprovedChannel) error {
	return b.Svc.ChannelApproved(ctx, domain.ChannelRef{ID: ch.ID, OrganizationID: ch.OrganizationID, Title: ch.Title, Link: ch.Link})
}

// ---------------------------------------------------------------- marking ← placement (отчётность)

// MarkingReportSource — marking/domain.ReportSource поверх выборок placement/postgres (R-19, R-20).
// Сумма рекламодателя — за вычетом возврата; неизвестный возврат — 0 (Q-44). Вознаграждение
// блогера — к выплате после итога, до него — по заказу.
type MarkingReportSource struct{ Store *placementpg.OrdReport }

var _ domain.ReportSource = MarkingReportSource{}

func (b MarkingReportSource) LivePublications(ctx context.Context, at time.Time) ([]domain.PublicationViews, error) {
	rows, err := b.Store.LiveViews(ctx, at)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PublicationViews, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.PublicationViews{PublicationID: r.PublicationID, Views: r.Views, CheckedAt: r.CheckedAt})
	}
	return out, nil
}

func (b MarkingReportSource) Placements(ctx context.Context, period domain.Period) ([]domain.SourcePlacement, error) {
	rows, err := b.Store.Placements(ctx, period.Start(), period.End())
	if err != nil {
		return nil, err
	}
	out := make([]domain.SourcePlacement, 0, len(rows))
	for _, r := range rows {
		p := domain.SourcePlacement{
			PlacementID: r.PlacementID, PublicationID: r.PublicationID, ChannelID: r.ChannelID, PostVersionID: r.PostVersionID,
			AdvertiserOrgID: r.AdvertiserOrganizationID, BloggerOrgID: r.BloggerOrganizationID, PublishedAt: r.PublishedAt,
			AdvertiserAmount: r.TotalAmount, BloggerAmount: r.BloggerAmount,
		}
		if r.RemovedAt != nil {
			p.RemovedAt = *r.RemovedAt
		}
		switch {
		case r.FinalViews != nil:
			p.FinalViews = *r.FinalViews
		case r.Views != nil:
			p.FinalViews = *r.Views
		}
		if r.RefundAmount != nil {
			p.AdvertiserAmount = max(r.TotalAmount-*r.RefundAmount, 0)
		}
		if r.PayoutAmount != nil {
			p.BloggerAmount = *r.PayoutAmount
		}
		out = append(out, p)
	}
	return out, nil
}
