// Package seed — начальные данные тестового сервера. Все названия, ИНН и
// адреса выдуманы; почты — в домене example.test. Идентификаторы фиксированы,
// чтобы на них можно было ссылаться из примеров спецификации и тестов.
package seed

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// ID возвращает фиксированный идентификатор начальных данных с номером n.
func ID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("0190a000-0000-7000-8000-%012d", n))
}

// Идентификаторы, на которые ссылаются README и тесты.
var (
	UserAdvertiser = ID(1)
	UserBlogger    = ID(2)
	UserAdmin      = ID(3)
	UserModerator  = ID(4)
	UserAdmin2     = ID(5)

	OrgAdvertiser = ID(11)
	OrgBlogger    = ID(12)
	OrgPlatform   = ID(13)
	OrgBlogger2   = ID(14)
	OrgMedia      = ID(15) // юрлицо-блогер: выплаты переводом (A-36)

	CampaignDraft   = ID(601)
	CampaignActive  = ID(602)
	CampaignPast    = ID(603)
	OrderActive     = ID(701)
	OrderPast       = ID(702)
	InvoicePaid     = ID(901)
	InvoiceIssued   = ID(902)
	ClosingActPast  = ID(951)
	CartItemFirst   = ID(301)
	MediaFirst      = ID(401)
	LegalAdvOffer   = ID(501)
	CategoryFood    = ID(201)
	ChannelFree     = ID(108) // активный канал, которого нет в корзине
	ChannelBlogger1 = ID(101)
	ChannelModerate = ID(105)
	ChannelQueue    = ID(113) // в очереди модерации, номер РКН ждёт сверки

	PlacementAwaitingBlogger  = ID(801)
	PlacementScheduled        = ID(802)
	PlacementPublished        = ID(803)
	PlacementRemoved          = ID(804)
	PlacementDisputed         = ID(805)
	PlacementPaid             = ID(806)
	PlacementAwaitingSchedule = ID(807)
	PlacementPayoutFailed     = ID(808) // принято, выплата не прошла
	PlacementBankTransfer     = ID(809) // принято, выплата юрлицу переводом ждёт отметки

	DisputeOpen        = ID(1601) // спор по размещению 805
	PayoutFailed       = ID(1202)
	PayoutBankTransfer = ID(1203)
	BankStatementFirst = ID(1301)
	BankTxUnmatched    = ID(1312)
	OrdContractError   = ID(1402)
)

// Emails готовых пользователей; код входа — 000000.
const (
	EmailAdvertiser = "adv@example.test"
	EmailBlogger    = "blg@example.test"
	EmailAdmin      = "adm@example.test"
	EmailModerator  = "mod@example.test"
	EmailAdmin2     = "adm2@example.test"
)

func ptr[T any](v T) *T { return &v }

// Load заполняет пустое состояние. Времена отсчитываются от now, поэтому
// после перезапуска или POST /_mock/reset сценарии остаются актуальными.
func Load(d *store.Data, now time.Time) {
	now = now.UTC().Truncate(time.Second)
	day := func(n int) time.Time {
		t := now.In(store.MSK).AddDate(0, 0, n)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	in := func(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }

	// ------------------------------------------------ пользователи и организации
	d.Orgs[OrgAdvertiser] = &store.Organization{
		ID: OrgAdvertiser, LegalType: "legal_entity", INN: "7700000001", Name: "ООО «Тестовый рекламодатель»",
		ContactName: "Иван Тестов", Phone: "+79990000001", KPP: ptr("770001001"),
		Address: ptr("г. Москва, ул. Примерная, д. 1"), TaxRegime: ptr("usn"), VatRate: ptr("none"),
		SignerPosition: ptr("Генеральный директор"), SignerName: ptr("Тестов Иван Иванович"), SignerBasisName: ptr("Устав"),
		BankAccount: ptr("40702810000000000001"), BankName: ptr("АО «Тестовый банк»"),
		BankCorrAccount: ptr("30101810000000000001"), BankBik: ptr("044525001"),
		Status: "active", IsAdvertiser: true, CreatedAt: day(-40),
	}
	d.Orgs[OrgBlogger] = &store.Organization{
		ID: OrgBlogger, LegalType: "sole_proprietor", INN: "770000000002", Name: "ИП Пример Мария Сергеевна",
		ContactName: "Мария Пример", Phone: "+79990000002", Address: ptr("г. Казань, ул. Образцовая, д. 2"),
		TaxRegime: ptr("usn"), VatRate: ptr("none"), BloggerFullName: ptr("Пример Мария Сергеевна"),
		Status: "active", IsBlogger: true, CreatedAt: day(-60),
	}
	d.Orgs[OrgPlatform] = &store.Organization{
		ID: OrgPlatform, LegalType: "sole_proprietor", INN: "770000000003", Name: "ИП Площадка Тестовая",
		ContactName: "Оператор", Phone: "+79990000003", Status: "active", CreatedAt: day(-90),
	}
	d.Orgs[OrgBlogger2] = &store.Organization{
		ID: OrgBlogger2, LegalType: "self_employed", INN: "770000000004", Name: "Образцов Пётр Петрович",
		ContactName: "Пётр Образцов", Phone: "+79990000004", TaxRegime: ptr("npd"), VatRate: ptr("none"),
		BloggerFullName: ptr("Образцов Пётр Петрович"), Status: "active", IsBlogger: true, CreatedAt: day(-50),
	}
	d.Users[UserAdvertiser] = &store.User{ID: UserAdvertiser, Email: EmailAdvertiser, OrgID: ptr(OrgAdvertiser), CreatedAt: day(-40)}
	d.Users[UserBlogger] = &store.User{ID: UserBlogger, Email: EmailBlogger, OrgID: ptr(OrgBlogger), CreatedAt: day(-60)}
	d.Users[UserAdmin] = &store.User{ID: UserAdmin, Email: EmailAdmin, AdminRole: "senior", CreatedAt: day(-90)}
	d.Users[UserModerator] = &store.User{ID: UserModerator, Email: EmailModerator, AdminRole: "moderator", CreatedAt: day(-80)}
	d.Users[UserAdmin2] = &store.User{ID: UserAdmin2, Email: EmailAdmin2, AdminRole: "senior", CreatedAt: day(-80)}
	d.Orgs[OrgMedia] = &store.Organization{
		ID: OrgMedia, LegalType: "legal_entity", INN: "7700000005", Name: "ООО «Тестовые медиа»",
		ContactName: "Анна Медиа", Phone: "+79990000005", KPP: ptr("770001005"), TaxRegime: ptr("osno"), VatRate: ptr("vat_22"),
		Status: "active", IsBlogger: true, CreatedAt: day(-45),
	}
	d.Rocketwork[OrgBlogger] = &store.RocketworkContractor{Status: "active", OnboardingURL: "https://rocketwork.example.test/onboarding/12"}

	// ------------------------------------------------ документы
	docs := []struct{ kind, title string }{
		{"advertiser_offer", "Оферта для рекламодателя"},
		{"blogger_offer", "Оферта для блогера"},
		{"platform_rules", "Правила площадки"},
		{"privacy_policy", "Политика обработки персональных данных"},
	}
	for i, doc := range docs {
		d.LegalDocs = append(d.LegalDocs, &store.LegalDocument{
			ID: ID(501 + i), Kind: doc.kind, Version: 1, PublishedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Title: doc.title,
			URL: "https://example.test/legal/" + doc.kind,
		})
	}
	d.Acceptances[OrgAdvertiser] = map[uuid.UUID]time.Time{ID(501): day(-40), ID(503): day(-40), ID(504): day(-40)}
	d.Acceptances[OrgBlogger] = map[uuid.UUID]time.Time{ID(502): day(-60), ID(503): day(-60), ID(504): day(-60)}

	// ------------------------------------------------ справочники
	for i, c := range []struct{ slug, name string }{
		{"food", "Еда и кулинария"}, {"tech", "Технологии"}, {"travel", "Путешествия"}, {"business", "Бизнес"},
		{"kids", "Дети и родители"}, {"sport", "Спорт"}, {"auto", "Авто"}, {"humor", "Юмор"},
	} {
		d.Categories = append(d.Categories, &store.Category{ID: ID(201 + i), Slug: c.slug, Name: c.name, Position: i})
	}
	d.Formats = []*store.Format{
		{ID: "f_1_24", Name: "1/24", TopHours: 1, DurationHours: 24, Position: 0},
		{ID: "f_2_48", Name: "2/48", TopHours: 2, DurationHours: 48, Position: 1},
		{ID: "f_3_72", Name: "3/72", TopHours: 3, DurationHours: 72, Position: 2},
	}

	// ------------------------------------------------ каналы
	type ch struct {
		n        int
		org      uuid.UUID
		title    string
		cat      int
		subs     int
		views    int
		status   string
		price    int64 // цена 1/24, копейки; 0 — без цен
		rkn      string
		anomaly  []string
		reason   string
		category bool
	}
	channels := []ch{
		{101, OrgBlogger, "Тестовый канал о кулинарии", 201, 8400, 2900, "active", 300000, "", nil, "", true},
		{102, OrgBlogger, "Выдуманные рецепты", 201, 5200, 1700, "active", 200000, "", nil, "", true},
		{103, OrgBlogger, "Пример: путешествия по выходным", 203, 9100, 2600, "active", 350000, "", nil, "", true},
		{104, OrgBlogger, "Пример: гаджеты недели", 202, 25000, 6100, "active", 900000, "4900000001", nil, "", true},
		{105, OrgBlogger, "Новый тестовый канал", 0, 1200, 300, "moderation", 0, "", nil, "", false},
		{106, OrgBlogger, "Отклонённый тестовый канал", 208, 3000, 400, "rejected", 100000, "", nil, "Описание не соответствует содержанию канала", true},
		{107, OrgBlogger2, "Образцовый бизнес", 204, 15000, 3800, "active", 600000, "4900000002", nil, "", true},
		{108, OrgBlogger2, "Тестовые новости спорта", 206, 7300, 2100, "active", 250000, "", nil, "", true},
		{109, OrgBlogger2, "Пример: родителям на заметку", 205, 4100, 1500, "active", 150000, "", nil, "", true},
		{110, OrgBlogger2, "Выдуманный автоклуб", 207, 12000, 900, "active", 400000, "4900000003", []string{"views_disproportion"}, "", true},
		{111, OrgBlogger2, "Шутки для теста", 208, 6600, 2500, "active", 180000, "", []string{"subscribers_spike"}, "", true},
		{112, OrgBlogger2, "Пример: код и кофе", 202, 9900, 3300, "active", 320000, "", nil, "", true},
		// Ждёт модератора: всё заполнено, но номер РКН ещё не сверен — одобрение после rkn-check.
		{113, OrgBlogger2, "Пример: очередь модерации", 203, 14000, 3500, "moderation", 450000, "4900000004", nil, "rkn_required", true},
		// Канал юрлица: приостановлен, в каталоге не виден; по нему — выплата переводом.
		{114, OrgMedia, "Пример: медиа компании", 204, 11000, 2500, "suspended", 500000, "4900000005", nil, "missing_rights", true},
	}
	for i, c := range channels {
		org := c.org
		x := &store.Channel{
			ID: ID(c.n), OrgID: &org, MaxChatID: -70000000000 - int64(c.n), Title: c.title,
			MaxDescription: "Описание канала из MAX (выдуманное)", Description: "Тестовое описание для каталога: " + c.title,
			IconURL: fmt.Sprintf("https://placehold.co/128x128/png?text=%d", c.n),
			Link:    fmt.Sprintf("https://max.ru/test_channel_%d", c.n), IsPublic: true,
			Status: c.status, StatusReason: c.reason, BotIsAdmin: true, BotPermissions: []string{"read", "write", "delete", "pin"},
			Subscribers: c.subs, AvgViews: c.views, PostsPerWeek: float64(7 + i%5), RknStatus: "not_required",
			Rating: 100 - i%4*5, AnomalyFlags: c.anomaly, Prices: map[string]*store.Price{}, CreatedAt: day(-30 - i),
		}
		x.MetricsHistory = demoMetrics(now, x.Subscribers, x.AvgViews)
		if c.category {
			x.CategoryID = ptr(ID(c.cat))
		}
		if c.rkn != "" {
			x.RknNumber, x.RknStatus = c.rkn, "confirmed"
			x.RknCheckedAt = ptr(day(-20))
		}
		if c.price > 0 {
			x.Prices["f_1_24"] = &store.Price{Amount: c.price, IsEnabled: true}
			x.Prices["f_2_48"] = &store.Price{Amount: c.price * 3 / 2, IsEnabled: true}
			x.Prices["f_3_72"] = &store.Price{Amount: c.price * 2, IsEnabled: c.n%2 == 0}
		}
		d.Channels = append(d.Channels, x)
	}
	d.Channels[3].RknCheckedAt = ptr(day(-100))                            // 104: пора проверить номер снова (rkn_recheck_due)
	d.Channels[12].RknStatus, d.Channels[12].RknCheckedAt = "pending", nil // 113
	d.Channels[2].Unavailability = []store.Unavailability{{ID: ID(1001), StartsAt: day(10), EndsAt: day(12)}}

	// ------------------------------------------------ корзина и медиатека
	cartTotal := func(channelN int, format string) int64 {
		for _, c := range d.Channels {
			if c.ID == ID(channelN) {
				_, t := store.TotalFor(c.Prices[format].Amount)
				return t
			}
		}
		panic("seed: channel not found")
	}
	d.CartItems = []*store.CartItem{
		{ID: CartItemFirst, OrgID: OrgAdvertiser, ChannelID: ID(101), FormatID: "f_1_24", ShownTotal: cartTotal(101, "f_1_24"), CreatedAt: ago(5)},
		{ID: ID(302), OrgID: OrgAdvertiser, ChannelID: ID(107), FormatID: "f_2_48", ShownTotal: cartTotal(107, "f_2_48"), CreatedAt: ago(4)},
	}
	for i, name := range []string{"sale-banner.png", "product.jpg", "logo.webp"} {
		mime := map[int]string{0: "image/png", 1: "image/jpeg", 2: "image/webp"}[i]
		d.Media = append(d.Media, &store.MediaFile{
			ID: ID(401 + i), OrgID: OrgAdvertiser, Name: name, Mime: mime, SizeBytes: int64(200_000 + i*150_000),
			Width: 1080, Height: 1080, CreatedAt: day(-10 + i),
		})
	}

	// ------------------------------------------------ кампании
	doc := func(title, text string) api.EditorDoc {
		bold := []api.EditorMark{{Type: "bold"}}
		link := []api.EditorMark{{Type: "link", Href: ptr("https://shop.example.test/sale")}}
		return api.EditorDoc{Type: "doc", Content: []api.EditorBlock{
			{Type: "heading", Content: &[]api.EditorText{{Type: "text", Text: title}}},
			{Type: "paragraph", Content: &[]api.EditorText{
				{Type: "text", Text: text + " "},
				{Type: "text", Text: "Только неделю.", Marks: &bold},
			}},
			{Type: "paragraph", Content: &[]api.EditorText{{Type: "text", Text: "Подробнее на сайте", Marks: &link}}},
		}}
	}
	campaign := func(id uuid.UUID, name, status string, from, to int, post api.EditorDoc, created time.Time) *store.Campaign {
		df, dt := day(from), day(to)
		return &store.Campaign{
			ID: id, OrgID: OrgAdvertiser, Name: name, Status: status, DateFrom: &df, DateTo: &dt,
			TimeFrom: ptr("00:00"), TimeTo: ptr("23:59"), ExtraRequirements: ptr("Не публиковать рядом с рекламой конкурентов"),
			Post:      store.PostDraft{Doc: post, MediaIDs: []uuid.UUID{MediaFirst}, TokenCarrier: "media", TokenPosition: "below", UpdatedAt: created},
			CreatedAt: created, UpdatedAt: created,
		}
	}
	d.Campaigns = []*store.Campaign{
		campaign(CampaignDraft, "Осенняя распродажа", "draft", 1, 6, doc("Осенняя распродажа", "Скидки до 50% на всё."), ago(3)),
		campaign(CampaignActive, "Запуск нового продукта", "ordered", -1, 5, doc("Новый продукт", "Тестовый продукт уже в продаже."), ago(30)),
		campaign(CampaignPast, "Летние скидки", "ordered", -5, 1, doc("Летние скидки", "Последние дни летних скидок."), ago(130)),
	}
	d.Campaigns[1].OrderID = ptr(OrderActive)
	d.Campaigns[2].OrderID = ptr(OrderPast)

	// ------------------------------------------------ баланс
	d.Invoices = []*store.Invoice{
		{ID: InvoicePaid, Number: "MX-000101", OrgID: OrgAdvertiser, Amount: 100_000_000, Status: "paid",
			ExpiresAt: day(-6), PaidAt: ptr(day(-19)), PaidAmount: ptr(int64(100_000_000)), CreatedAt: day(-20)},
		{ID: InvoiceIssued, Number: "MX-000102", OrgID: OrgAdvertiser, Amount: 5_000_000, Status: "issued",
			ExpiresAt: in(24 * 13), CreatedAt: ago(24)},
	}
	d.NextInvoiceNo = 103
	d.ClosingActs = []*store.ClosingAct{
		{ID: ClosingActPast, OrgID: OrgAdvertiser, Period: day(-35).Format("2006-01"), Number: "А-000001",
			PlacementsAmount: 1_500_000, FeeAmount: 225_000, CreatedAt: day(-25)},
	}
	d.Ledger = append(d.Ledger, &store.LedgerEntry{ID: ID(1101), OrgID: OrgAdvertiser, Kind: "deposit", Amount: 100_000_000,
		Description: "Пополнение по счёту MX-000101", InvoiceID: ptr(InvoicePaid), CreatedAt: day(-19)})

	// ------------------------------------------------ заказы и размещения
	order := func(id uuid.UUID, number int64, camp *store.Campaign, versionID uuid.UUID, erid string, created time.Time) *store.Order {
		body := store.Serialize(camp.Post.Doc)
		adv := d.Orgs[OrgAdvertiser]
		v := &store.PostVersion{
			ID: versionID, Number: 1, Status: "ready", Doc: camp.Post.Doc, Body: body, Erid: erid,
			FinalText: store.FinalText(body, "media", "below", adv.Name, adv.INN, erid),
			MediaIDs:  camp.Post.MediaIDs, TokenCarrier: "media", TokenPosition: "below",
			AdvertiserName: adv.Name, AdvertiserINN: adv.INN, CreatedAt: created,
		}
		return &store.Order{ID: id, Number: number, OrgID: OrgAdvertiser, CampaignID: camp.ID,
			Versions: []*store.PostVersion{v}, Status: "in_progress", CreatedAt: created}
	}
	oActive := order(OrderActive, 1001, d.Campaigns[1], ID(751), "MockA1B2C3D4", ago(30))
	oPast := order(OrderPast, 1002, d.Campaigns[2], ID(752), "MockE5F6A7B8", ago(130))
	d.Orders = []*store.Order{oActive, oPast}
	d.NextOrderNo = 1003

	type step struct {
		code, to string
		hoursAgo float64
	}
	placement := func(id uuid.UUID, o *store.Order, channelN int, format string, steps []step) *store.Placement {
		var c *store.Channel
		for _, x := range d.Channels {
			if x.ID == ID(channelN) {
				c = x
			}
		}
		var f *store.Format
		for _, x := range d.Formats {
			if x.ID == format {
				f = x
			}
		}
		camp := d.Campaigns[1]
		if o == oPast {
			camp = d.Campaigns[2]
		}
		ws, we := store.WindowFor(*camp.DateFrom, *camp.DateTo)
		fee, total := store.TotalFor(c.Prices[format].Amount)
		p := &store.Placement{
			ID: id, OrderID: o.ID, ChannelID: c.ID, BloggerOrgID: *c.OrgID, FormatID: f.ID,
			TopHours: f.TopHours, DurationHours: f.DurationHours, WindowStart: ws, WindowEnd: we,
			TimeFrom: *camp.TimeFrom, TimeTo: *camp.TimeTo, PostVersionID: o.Versions[0].ID,
			FeeRateBP: store.FeeRateBP, BloggerAmount: c.Prices[format].Amount, FeeAmount: fee, TotalAmount: total,
			HoldActive: true, CreatedAt: o.CreatedAt,
		}
		from := ""
		for _, s := range steps {
			actor := map[string]string{"P-01": "advertiser", "P-03": "blogger", "P-17": "blogger", "P-12": "advertiser", "P-14": "advertiser"}[s.code]
			if actor == "" {
				actor = "system"
			}
			p.History = append(p.History, store.HistoryEntry{Transition: s.code, From: from, To: s.to, ActorType: actor, CreatedAt: ago(s.hoursAgo)})
			from = s.to
		}
		last := steps[len(steps)-1]
		p.Status, p.StatusChangedAt = last.to, ago(last.hoursAgo)
		o.TotalAmount += total
		d.Placements = append(d.Placements, p)
		oid, pid := o.ID, p.ID
		d.Ledger = append(d.Ledger, &store.LedgerEntry{ID: store.NewID(), OrgID: OrgAdvertiser, Kind: "hold", Amount: -total,
			Description: fmt.Sprintf("Удержание: заказ %d, канал «%s»", o.Number, c.Title), OrderID: &oid, PlacementID: &pid, CreatedAt: o.CreatedAt})
		return p
	}
	start := []step{{"P-01", "created", 30}, {"P-02", "awaiting_blogger", 28}}
	p801 := placement(PlacementAwaitingBlogger, oActive, 102, "f_1_24", start)
	p801.AcceptDeadlineAt = ptr(in(20))

	p807 := placement(PlacementAwaitingSchedule, oActive, 104, "f_2_48", append(start, step{"P-03", "awaiting_schedule", 6}))
	p807.ScheduleDeadlineAt = ptr(in(18))

	p802 := placement(PlacementScheduled, oActive, 103, "f_1_24", append(start, step{"P-03", "awaiting_schedule", 20}, step{"P-17", "scheduled", 19}))
	p802.PublishAt, p802.RemoveAt = ptr(in(3)), ptr(in(27))

	p803 := placement(PlacementPublished, oActive, 107, "f_2_48", append(start, step{"P-03", "awaiting_schedule", 26}, step{"P-17", "scheduled", 25}, step{"P-08", "published", 10}))
	p803.PublishAt, p803.PublishedAt, p803.RemoveAt = ptr(ago(10)), ptr(ago(10)), ptr(in(38))
	p803.Views, p803.PublicationURL = ptr(2400), "https://max.ru/c/70000000107/31001"

	past := []step{{"P-01", "created", 130}, {"P-02", "awaiting_blogger", 128}, {"P-03", "awaiting_schedule", 120}, {"P-17", "scheduled", 119}, {"P-08", "published", 100}}
	p804 := placement(PlacementRemoved, oPast, 101, "f_1_24", append(past, step{"P-10", "removed", 4}))
	p804.PublishAt, p804.PublishedAt, p804.RemoveAt, p804.RemovedAt = ptr(ago(100)), ptr(ago(100)), ptr(ago(76)), ptr(ago(4))
	p804.ClaimDeadlineAt, p804.Views, p804.PublicationURL = ptr(in(20)), ptr(3100), "https://max.ru/c/70000000101/20501"

	p805 := placement(PlacementDisputed, oPast, 108, "f_1_24", append(past, step{"P-14", "disputed", 90}))
	p805.PublishAt, p805.PublishedAt, p805.RemoveAt = ptr(ago(100)), ptr(ago(100)), ptr(ago(76))
	p805.Views, p805.PublicationURL = ptr(800), "https://max.ru/c/70000000108/12002"
	p805.Claim = &store.Claim{Reason: "removed_early", Text: "Пост удалён через 10 часов после публикации", CreatedAt: ago(90)}
	p805.Dispute = &store.Dispute{Reason: "removed_early", OpenedByType: "advertiser", OpenedAt: ago(90), DeadlineAt: ago(90).Add(store.DisputeDeadline)}
	p805.Dispute.ID, p805.Dispute.SuggestedShare = DisputeOpen, ptr(4200)
	p805.RemovedAt, p805.RemovalReason = ptr(ago(90)), "external"
	p805.Violations = []store.Violation{{Kind: "removed_early", OccurredAt: ago(90), Source: "event"}}

	p806 := placement(PlacementPaid, oPast, 102, "f_1_24", append(past, step{"P-10", "removed", 76}, step{"P-12", "accepted", 70}, step{"P-16", "paid", 60}))
	p806.PublishAt, p806.PublishedAt, p806.RemoveAt, p806.RemovedAt = ptr(ago(100)), ptr(ago(100)), ptr(ago(76)), ptr(ago(76))
	p806.Views, p806.PublicationURL, p806.HoldActive = ptr(1900), "https://max.ru/c/70000000102/15003", false
	share, payout, fee, refund := 10000, p806.BloggerAmount, p806.FeeAmount, int64(0)
	p806.PerformanceShare, p806.PayoutAmount, p806.FeeCharged, p806.RefundAmount = &share, &payout, &fee, &refund
	d.Payouts = append(d.Payouts, &store.Payout{ID: ID(1201), PlacementID: p806.ID, OrgID: OrgBlogger, Amount: payout,
		Status: "succeeded", Attempt: 1, ReceiptURL: "https://rocketwork.example.test/receipts/1201",
		CreatedAt: ago(70), CompletedAt: ptr(ago(60))})

	// Принятые размещения с выплатами для реестра администратора.
	accepted := append(past, step{"P-10", "removed", 76}, step{"P-12", "accepted", 70})
	for _, x := range []struct {
		id      uuid.UUID
		channel int
		payout  *store.Payout
	}{
		{PlacementPayoutFailed, 109, &store.Payout{ID: PayoutFailed, OrgID: OrgBlogger2, Status: "failed", Attempt: 1, Method: "rocketwork",
			FailureCode: "tax_status_lost", FailureMessage: "Исполнитель утратил статус самозанятого", CreatedAt: ago(69)}},
		{PlacementBankTransfer, 114, &store.Payout{ID: PayoutBankTransfer, OrgID: OrgMedia, Status: "created", Attempt: 1, Method: "bank_transfer", CreatedAt: ago(69)}},
	} {
		p := placement(x.id, oPast, x.channel, "f_1_24", accepted)
		p.PublishAt, p.PublishedAt, p.RemoveAt, p.RemovedAt = ptr(ago(100)), ptr(ago(100)), ptr(ago(76)), ptr(ago(76))
		p.RemovalReason, p.Views, p.HoldActive = "schedule", ptr(1200), false
		share, payout, fee, refund := 10000, p.BloggerAmount, p.FeeAmount, int64(0)
		p.PerformanceShare, p.PayoutAmount, p.FeeCharged, p.RefundAmount = &share, &payout, &fee, &refund
		x.payout.PlacementID, x.payout.Amount = p.ID, payout
		d.Payouts = append(d.Payouts, x.payout)
	}
	loadAdmin(d, now, ago)

	// ------------------------------------------------ уведомления
	// Коды и ссылки — как у бэкенда (каталог уведомлений, 04 §8; notification/domain.Link).
	note := func(user uuid.UUID, tpl, title, text, objType string, obj uuid.UUID, order *uuid.UUID, link string, hoursAgo float64, read bool) {
		n := &store.Notification{ID: store.NewID(), UserID: user, Template: tpl, Title: title, Text: text,
			ObjectType: ptr(objType), ObjectID: ptr(obj), OrderID: order, Link: link, CreatedAt: ago(hoursAgo)}
		if read {
			n.ReadAt = ptr(ago(hoursAgo - 0.5))
		}
		d.Notifications = append(d.Notifications, n)
	}
	advOrder := func(o uuid.UUID) string { return "/adv/order/" + o.String() }
	blgPlacement := func(p uuid.UUID) string { return "/blg/placement/" + p.String() }
	note(UserAdvertiser, "placement.removed", "Пост снят из «Тестовый канал о кулинарии»",
		"Размещение по заказу № 1001 завершено: пост снят из «Тестовый канал о кулинарии».\nПросмотров: 2 400.\nПретензию можно открыть в течение 24 часов, после этого размещение будет принято автоматически.",
		"placement", p804.ID, ptr(OrderActive), advOrder(OrderActive), 4, false)
	note(UserAdvertiser, "placement.published", "Пост опубликован в «Образцовый бизнес»",
		"Пост по заказу № 1001 вышел в «Образцовый бизнес».", "placement", p803.ID, ptr(OrderActive), advOrder(OrderActive), 10, true)
	note(UserAdvertiser, "order.post_approved", "Пост заказа № 1001 одобрен",
		"Модератор одобрил пост заказа № 1001. Запросы отправлены блогерам.", "order", OrderActive, ptr(OrderActive), advOrder(OrderActive), 30, true)
	note(UserAdvertiser, "balance.deposited", "Баланс пополнен на 150 000 ₽",
		"На баланс зачислено 150 000 ₽ по счёту СЧ-2026-000001.", "invoice", InvoicePaid, nil, "/adv/balance", 50, true)
	note(UserBlogger, "placement.request", "Новый запрос на размещение в «Выдуманные рецепты»",
		"Рекламодатель хочет разместить пост в «Выдуманные рецепты», заказ № 1001.\nОтветьте в течение суток, иначе запрос истечёт.",
		"placement", p801.ID, ptr(OrderActive), blgPlacement(p801.ID), 28, false)
	note(UserBlogger, "channel.approved", "Канал «Выдуманные рецепты» прошёл модерацию",
		"Канал «Выдуманные рецепты» опубликован в каталоге.", "channel", ChannelBlogger1, nil, "/blg/channel/"+ChannelBlogger1.String(), 200, true)
	note(UserBlogger, "placement.disputed", "Претензия по размещению в «Выдуманные рецепты»",
		"Рекламодатель открыл претензию по заказу № 1002. Платформа рассмотрит её и сообщит решение.",
		"placement", p805.ID, ptr(OrderPast), blgPlacement(p805.ID), 90, true)
	note(UserBlogger, "payout.succeeded", "Выплата получена",
		"Деньги за размещение в «Выдуманные рецепты», заказ № 1002, выплачены.", "placement", p806.ID, ptr(OrderPast), blgPlacement(p806.ID), 60, true)
	note(UserAdmin, "placement.disputed", "Претензия по размещению",
		"Рекламодатель открыл претензию по заказу № 1002. Нужно решение администратора.", "placement", p805.ID, ptr(OrderPast), "/adm/placement/"+p805.ID.String(), 90, false)
}
