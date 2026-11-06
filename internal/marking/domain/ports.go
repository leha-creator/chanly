package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Receipt — ответ ОРД на POST и GET /status (ResponseStatus).
type Receipt struct {
	RequestID      string
	Status         string
	Token          string
	ObjectStatuses []ObjectStatus
	Errors         []string
}

// ObjectStatus — статус одного объекта запроса.
type ObjectStatus struct {
	ObjectID string
	Status   string
	Token    string
	Errors   []string
}

// For — статус, токен и ошибки объекта externalID: из object_statuses[] по нашему
// идентификатору, иначе общие (R-3).
func (r Receipt) For(externalID string) (status, token string, errs []string) {
	status, token = r.Status, r.Token
	errs = append([]string(nil), r.Errors...)
	for _, o := range r.ObjectStatuses {
		errs = append(errs, o.Errors...)
		if o.ObjectID == externalID && o.Token != "" {
			token = o.Token
		}
		current, child := ClassifyOrdStatus(status, true), ClassifyOrdStatus(o.Status, true)
		if current != ProgressFinalError && (child == ProgressFinalError || child < current) {
			status = o.Status
		}
	}
	return
}

// OrdAPI — Яндекс ОРД, API v8 (05 §3). Реализация: integration/ord (переходник в bootstrap/bridge)
// или ordfake. Вызывается только из обработчиков заданий вне транзакции (I-13). Ошибки —
// в терминах планировщика: job.Permanent — отказ ОРД (4xx, 422), прочие — повтор.
type OrdAPI interface {
	UpsertOrganization(ctx context.Context, req OrganizationRequest) (Receipt, error)
	UpsertContract(ctx context.Context, req ContractRequest) (Receipt, error)
	UpsertPlatform(ctx context.Context, req PlatformRequest) (Receipt, error)
	UpsertCreative(ctx context.Context, req CreativeRequest) (Receipt, error)
	// UpsertStatistics — строка статистики месяца (R-20).
	UpsertStatistics(ctx context.Context, req StatisticsRequest) (Receipt, error)
	// Each report operation returns its own receipt and is persisted independently.
	UpsertInvoice(ctx context.Context, req InvoiceRequest) (Receipt, error)
	UpsertInvoiceCreatives(ctx context.Context, req InvoiceRequest) (Receipt, error)
	GetStatus(ctx context.Context, requestID string) (Receipt, error)
}

// Organizations — реквизиты организаций для ОРД. Реализация: переходник над account/postgres.
type Organizations interface {
	// Details — реквизиты организации; ErrNotFound — нет.
	Details(ctx context.Context, orgID uuid.UUID) (OrgDetails, error)
	// Platform — организация-платформа (organization.is_platform, R-5); ErrNotFound — нет.
	Platform(ctx context.Context) (OrgDetails, error)
	// FirstOfferAcceptance — первое принятие оферты вида kind (advertiser_offer, blogger_offer);
	// false — организация её не принимала.
	FirstOfferAcceptance(ctx context.Context, orgID uuid.UUID, kind string) (OfferAcceptance, bool, error)
}

// CreativeSource — содержание версии поста для креатива (R-8). Реализация: post/app.CreativeContent.
type CreativeSource interface {
	CreativeContent(ctx context.Context, versionID uuid.UUID) (CreativeContent, error)
}

// VersionMarker — перевод версии в ready с erid (post/app.MarkVersionReady) в транзакции
// вызывающего; последствия для размещений (P-02) выполняет слушатель версии модуля размещений.
type VersionMarker interface {
	MarkVersionReady(ctx context.Context, versionID uuid.UUID, erid string) error
}

// ChannelPlatformReady — площадка канала зарегистрирована: канал становится виден в каталоге
// (R-14). Реализация: channel/app.SetOrdPlatformReady.
type ChannelPlatformReady interface {
	SetOrdPlatformReady(ctx context.Context, channelID uuid.UUID, at time.Time) error
}

// CreativeListener — ЕРИР отклонил креатив с erid (S-28 шаг 2, R-12). Реализация:
// placement/app.CreativeRejected; вызывается в транзакции перехода креатива в error.
// published — уже опубликованные размещения версии: решение по ним принимает администратор.
type CreativeListener interface {
	CreativeRejected(ctx context.Context, versionID uuid.UUID) (published []uuid.UUID, err error)
}

// NopCreativeListener — модуль размещений не подключён.
type NopCreativeListener struct{}

func (NopCreativeListener) CreativeRejected(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// OrdOrganizations — записи ord_organization.
type OrdOrganizations interface {
	// ByOrganization — запись организации без блокировки; ErrNotFound — нет.
	ByOrganization(ctx context.Context, orgID uuid.UUID) (Organization, error)
	// GetForUpdate блокирует запись; ErrNotFound — нет.
	GetForUpdate(ctx context.Context, id uuid.UUID) (Organization, error)
	// Insert вставляет запись; при гонке возвращает уже существующую и created = false.
	Insert(ctx context.Context, o Organization) (rec Organization, created bool, err error)
	Save(ctx context.Context, o Organization) error
}

// OrdContracts — записи ord_contract.
type OrdContracts interface {
	// ByParties — договор вида по сторонам без блокировки; ErrNotFound — нет.
	ByParties(ctx context.Context, kind ContractKind, client, contractor uuid.UUID) (Contract, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (Contract, error)
	Insert(ctx context.Context, c Contract) (rec Contract, created bool, err error)
	Save(ctx context.Context, c Contract) error
}

// OrdPlatforms — записи ord_platform.
type OrdPlatforms interface {
	ByChannel(ctx context.Context, channelID uuid.UUID) (Platform, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (Platform, error)
	Insert(ctx context.Context, p Platform) (rec Platform, created bool, err error)
	Save(ctx context.Context, p Platform) error
}

// Creatives — записи creative.
type Creatives interface {
	ByVersion(ctx context.Context, versionID uuid.UUID) (Creative, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (Creative, error)
	Insert(ctx context.Context, c Creative) (rec Creative, created bool, err error)
	Save(ctx context.Context, c Creative) error
	// Delete удаляет креатив без отправки (R-11).
	Delete(ctx context.Context, id uuid.UUID) error
}

// ErrorItem — объект в error для списка S-20.
type ErrorItem struct {
	Ref        ObjectRef
	ExternalID string
	OrdStatus  string
	Error      string
	Attempts   int
	UpdatedAt  time.Time
	// LastRequestID — последний вызов ОРД по объекту в outbound_request; uuid.Nil — вызовов не было.
	LastRequestID uuid.UUID
}

// RequestLog — вызов ОРД из outbound_request: тела — как сохранены адаптером (ИНН с маской).
type RequestLog struct {
	ID             uuid.UUID
	Operation      string
	Attempt        int
	Request        []byte
	ResponseStatus *int
	Response       []byte
	Error          string
	CreatedAt      time.Time
}

// ErrorCursor — ключ страницы списка ошибок: (updated_at, id).
type ErrorCursor struct {
	Type      ObjectType
	UpdatedAt time.Time
	ID        uuid.UUID
}

// StuckObject — объект в sent дольше порога (07 §3).
type StuckObject struct {
	Ref    ObjectRef
	SentAt time.Time
}

// Overview — выборки по всем видам объектов: список S-20, зависшие, метрика.
type Overview interface {
	// ListErrors — объекты в error по (updated_at, id) после курсора.
	ListErrors(ctx context.Context, typ ObjectType, after *ErrorCursor, limit int) ([]ErrorItem, error)
	// LastRequest — последний вызов ОРД по объекту; nil — вызовов не было (этап «API администратора», R-17).
	LastRequest(ctx context.Context, ref ObjectRef, externalID string) (*RequestLog, error)
	// StuckSent — объекты в sent с sent_at раньше before.
	StuckSent(ctx context.Context, before time.Time, limit int) ([]StuckObject, error)
	// CreativesWithoutErid — креативы без erid, созданные раньше before (T-11).
	CreativesWithoutErid(ctx context.Context, before time.Time, limit int) ([]Creative, error)
	// CountByStatus — число объектов по виду и состоянию.
	CountByStatus(ctx context.Context) (map[ObjectType]map[SyncStatus]int, error)
}

// Config — параметры модуля (07 §8).
type Config struct {
	// KKTUCode — код ККТУ всех креативов (ORD_KKTU_CODE, A-42, Q-39).
	KKTUCode string
	// PlatformType — тип площадки канала (ORD_PLATFORM_TYPE, A-32, Q-13).
	PlatformType string
	Contracts    ContractRules
	Poll         PollRules
	// DependencyWait — ожидание неготовой зависимости (ORD_DEPENDENCY_WAIT).
	DependencyWait time.Duration
	// DependencyWaitFailed — ожидание зависимости в error (ORD_DEPENDENCY_WAIT_FAILED).
	DependencyWaitFailed time.Duration
	// EridWait — креатив без erid дольше — оповещение T-11 (ORD_ERID_WAIT).
	EridWait time.Duration
	// StuckSentAge — объект в sent дольше — зависший (STUCK_ORD_SENT_AGE).
	StuckSentAge time.Duration
	// Report — ежемесячная отчётность (этап «API администратора и отчётность ОРД»).
	Report ReportRules
}

// WithDefaults подставляет значения по умолчанию.
func (c Config) WithDefaults() Config {
	if c.KKTUCode == "" {
		c.KKTUCode = "30.15.1" // только для разработки и тестов: в production задаётся явно (A-42)
	}
	if c.PlatformType == "" {
		c.PlatformType = "site" // A-32, Q-13
	}
	c.Contracts = c.Contracts.WithDefaults()
	c.Poll = c.Poll.WithDefaults()
	if c.DependencyWait <= 0 {
		c.DependencyWait = 30 * time.Second
	}
	if c.DependencyWaitFailed <= 0 {
		c.DependencyWaitFailed = 10 * time.Minute
	}
	if c.EridWait <= 0 {
		c.EridWait = time.Hour
	}
	if c.StuckSentAge <= 0 {
		c.StuckSentAge = 24 * time.Hour
	}
	c.Report = c.Report.WithDefaults()
	return c
}

// ---------------------------------------------------------------- ежемесячная отчётность (R-18 … R-24)

// SourcePlacement — размещение с публикацией из модуля размещений (переходник в bootstrap/bridge).
type SourcePlacement struct {
	FinalViewsKnown  bool
	CommissionAmount int64
	PlacementID      uuid.UUID
	PublicationID    uuid.UUID
	ChannelID        uuid.UUID
	PostVersionID    uuid.UUID
	AdvertiserOrgID  uuid.UUID
	BloggerOrgID     uuid.UUID
	PublishedAt      time.Time
	RemovedAt        time.Time // нулевое — в канале
	FinalViews       int64
	AdvertiserAmount int64
	BloggerAmount    int64
}

// PublicationViews — показы живой публикации на момент снимка.
type PublicationViews struct {
	PublicationID uuid.UUID
	Views         int64
	CheckedAt     *time.Time
}

// ReportSource — данные размещений для отчёта.
type ReportSource interface {
	// LivePublications — публикации в канале на момент at.
	LivePublications(ctx context.Context, at time.Time) ([]PublicationViews, error)
	// Placements — размещения с публикацией, пересекающей месяц.
	Placements(ctx context.Context, period Period) ([]SourcePlacement, error)
}

// ReportState — состояние отчёта месяца (ord_report).
type ReportState struct {
	Period          Period
	BuiltAt         time.Time
	BuiltBy         *uuid.UUID
	SendRequestedAt *time.Time
	SendRequestedBy *uuid.UUID
	UpdatedAt       time.Time
}

// Reports — хранение отчётности: снимки, статистика, акты, состояние месяца.
type Reports interface {
	// InsertSnapshots сохраняет снимки начала месяца; уже снятые не меняются. Возвращает число новых.
	InsertSnapshots(ctx context.Context, period Period, views []PublicationViews, now time.Time) (int, error)
	// Snapshots — снимки месяца по публикациям.
	Snapshots(ctx context.Context, period Period) (map[uuid.UUID]int64, error)
	Statistics(ctx context.Context, period Period) ([]Statistics, error)
	StatisticsForUpdate(ctx context.Context, id uuid.UUID) (Statistics, error)
	InsertStatistics(ctx context.Context, s Statistics) error
	SaveStatistics(ctx context.Context, s Statistics) error
	DeleteStatistics(ctx context.Context, id uuid.UUID) error
	// Invoices — акты месяца с разбивкой.
	Invoices(ctx context.Context, period Period) ([]Invoice, error)
	InvoiceForUpdate(ctx context.Context, id uuid.UUID) (Invoice, error)
	InsertInvoice(ctx context.Context, i Invoice) error
	// SaveInvoice сохраняет акт и заменяет разбивку.
	SaveInvoice(ctx context.Context, i Invoice) error
	DeleteInvoice(ctx context.Context, id uuid.UUID) error
	// Report — состояние месяца; ErrNotFound — не формировался.
	Report(ctx context.Context, period Period) (ReportState, error)
	SaveReport(ctx context.Context, r ReportState) error
	// ReportList — сформированные месяцы, новые первыми.
	ReportList(ctx context.Context) ([]ReportState, error)
}
