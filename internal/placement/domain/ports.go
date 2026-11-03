package domain

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Порты модуля. Репозитории реализует placement/postgres; порты к другим модулям —
// переходники в bootstrap. Методы работают в транзакции из context, если она открыта.
// Кабинетные выборки принимают организацию явно (07 §4).

// Carts — позиции корзины организации.
type Carts interface {
	// List — позиции организации по времени добавления.
	List(ctx context.Context, orgID uuid.UUID) ([]CartItem, error)
	// Get — позиция организации; ErrNotFound для чужой.
	Get(ctx context.Context, orgID, id uuid.UUID) (CartItem, error)
	// Insert — новая позиция; канал уже в корзине — ErrAlreadyInCart.
	Insert(ctx context.Context, it CartItem) error
	// Update — смена формата и показанного итога.
	Update(ctx context.Context, it CartItem) error
	// Delete — удаление позиции организации; ErrNotFound для чужой.
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	// Clear — очистка корзины после оформления.
	Clear(ctx context.Context, orgID uuid.UUID) error
	Count(ctx context.Context, orgID uuid.UUID) (int, error)
	// ChannelsInCart — какие из каналов в корзине организации.
	ChannelsInCart(ctx context.Context, orgID uuid.UUID, channelIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// Orders — заказы.
type Orders interface {
	// NextNumber — следующий номер из order_number_seq (R-21).
	NextNumber(ctx context.Context) (int64, error)
	Insert(ctx context.Context, o Order) error
	// Get — заказ организации; ErrNotFound для чужого.
	Get(ctx context.Context, orgID, id uuid.UUID) (Order, error)
	// GetForUpdate — то же с блокировкой строки.
	GetForUpdate(ctx context.Context, orgID, id uuid.UUID) (Order, error)
	// GetByID — заказ по идентификатору без проверки организации (после проверки доступа к размещению).
	GetByID(ctx context.Context, id uuid.UUID) (Order, error)
	// GetByIDForUpdate — заказ по идентификатору с блокировкой (система: таймеры, события).
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (Order, error)
	// List — заказы организации, новые сверху, после курсора.
	List(ctx context.Context, orgID uuid.UUID, status *OrderStatus, after *PageCursor, limit int) ([]Order, error)
	// FindByIdempotencyKey — заказ организации, оформленный с ключом (R-18).
	FindByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (Order, bool, error)
	// FindByCurrentVersion — заказ, у которого версия текущая (R-5), без блокировки.
	FindByCurrentVersion(ctx context.Context, versionID uuid.UUID) (Order, bool, error)
	// FindByPlacementVersion — заказ, размещения которого привязаны к версии (этап «Маркировка», R-12).
	FindByPlacementVersion(ctx context.Context, versionID uuid.UUID) (Order, bool, error)
	// Update — состояние, текущая версия, ключ правки поста, updated_at.
	Update(ctx context.Context, o Order) error
}

// Placements — размещения.
type Placements interface {
	Insert(ctx context.Context, p Placement) error
	// Get — размещение по идентификатору без блокировки (система: узнать заказ и канал).
	Get(ctx context.Context, id uuid.UUID) (Placement, error)
	// GetForUpdate — размещение по идентификатору с блокировкой (система).
	GetForUpdate(ctx context.Context, id uuid.UUID) (Placement, error)
	// GetForAdvertiser — размещение заказа организации без блокировки; ErrNotFound для чужого.
	GetForAdvertiser(ctx context.Context, orgID, id uuid.UUID) (Placement, error)
	// GetForAdvertiserForUpdate — размещение заказа организации с блокировкой; ErrNotFound для чужого.
	GetForAdvertiserForUpdate(ctx context.Context, orgID, id uuid.UUID) (Placement, error)
	// GetForBlogger — размещение блогера, отправленное ему хотя бы раз (R-16); иначе ErrNotFound.
	GetForBlogger(ctx context.Context, orgID, id uuid.UUID) (Placement, error)
	// GetForBloggerForUpdate — то же с блокировкой строки.
	GetForBloggerForUpdate(ctx context.Context, orgID, id uuid.UUID) (Placement, error)
	// UpdateIfStatus сохраняет размещение, если в базе состояние status и lock_version
	// lockVersion (03 §11); иначе ErrStaleVersion.
	UpdateIfStatus(ctx context.Context, p Placement, status Status, lockVersion int) error
	// SetDistribution сохраняет распределение удержания (03 §7) один раз; уже задано — ErrStaleVersion.
	SetDistribution(ctx context.Context, id uuid.UUID, d Distribution) error
	// ListByOrder — размещения заказа по времени создания.
	ListByOrder(ctx context.Context, orderID uuid.UUID) ([]Placement, error)
	// ListByOrderForUpdate — то же с блокировкой строк в порядке id.
	ListByOrderForUpdate(ctx context.Context, orderID uuid.UUID) ([]Placement, error)
	// ListForBlogger — размещения блогера, отправленные ему, новые сверху, после курсора.
	ListForBlogger(ctx context.Context, orgID uuid.UUID, status *Status, channelID *uuid.UUID, after *PageCursor, limit int) ([]Placement, error)
	// BusyIntervals — интервалы топа размещений канала в scheduled и published, пересекающие
	// [from, to), кроме exclude (I-06).
	BusyIntervals(ctx context.Context, channelID uuid.UUID, from, to time.Time, exclude uuid.UUID) ([]Interval, error)
	// ListByChannel — размещения канала в состояниях statuses без блокировки.
	ListByChannel(ctx context.Context, channelID uuid.UUID, statuses []Status) ([]Placement, error)
	// ScheduledOverlapping — размещения канала в scheduled, у которых [publish_at, remove_at)
	// пересекает [from, to) (R-8 этапа «Каталог»).
	ScheduledOverlapping(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]uuid.UUID, error)
	// CountByStatus — число размещений по состояниям (07 §5.5).
	CountByStatus(ctx context.Context) (map[Status]int, error)
	// CountByOrders — число размещений заказов по состояниям.
	CountByOrders(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID]map[Status]int, error)
	// OverdueScheduled — размещения в scheduled с publish_at раньше before (07 §3).
	OverdueScheduled(ctx context.Context, before time.Time) ([]uuid.UUID, error)
	// ScheduledWithoutPublication — scheduled с publish_at раньше before без публикации (05 §2.8 шаг 5).
	ScheduledWithoutPublication(ctx context.Context, before time.Time) ([]ScheduledRef, error)
}

// History — история состояний (03 §12). Только добавление.
type History interface {
	Insert(ctx context.Context, h HistoryEntry) error
	// ListByPlacements — записи размещений по времени.
	ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID][]HistoryEntry, error)
	// FindByKey — запись размещения с ключом идемпотентности (R-18).
	FindByKey(ctx context.Context, placementID uuid.UUID, key string) (HistoryEntry, bool, error)
	// ExistsKeyInOrder — у одного из размещений заказа есть запись с ключом (R-17, R-18).
	ExistsKeyInOrder(ctx context.Context, orderID uuid.UUID, key string) (bool, error)
}

// Channels — каналы для оформления и действий блогера. Реализация — модуль каналов.
type Channels interface {
	// Offers — каналы с ценами и видимостью; отсутствующих в ответе нет.
	Offers(ctx context.Context, channelIDs []uuid.UUID) (map[uuid.UUID]ChannelOffer, error)
	// LockForPlacement — канал с блокировкой строки до конца транзакции (R-14, I-06, I-08);
	// ErrNotFound — канала нет.
	LockForPlacement(ctx context.Context, channelID uuid.UUID) (ChannelOffer, error)
	// Unavailability — интервалы недоступности канала, пересекающие [from, to).
	Unavailability(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]Interval, error)
}

// Campaigns — кампании. Реализация — модуль post (R-1).
type Campaigns interface {
	// LockForCheckout — кампания организации с блокировкой строки; ErrNotFound для чужой.
	LockForCheckout(ctx context.Context, orgID, campaignID uuid.UUID) (CampaignSnapshot, error)
	// Get — кампания организации (названия и требования для кабинетов).
	Get(ctx context.Context, orgID, campaignID uuid.UUID) (CampaignSnapshot, error)
	// MarkOrdered — draft → ordered со ссылкой на заказ.
	MarkOrdered(ctx context.Context, orgID, campaignID, orderID uuid.UUID) error
	// Archive — ordered → archived (R-19); уже архивная — без изменений.
	Archive(ctx context.Context, orgID, campaignID uuid.UUID) error
}

// VersionInfo — версия поста для ответов и правил модуля.
type VersionInfo struct {
	ID              uuid.UUID
	Number          int
	Status          VersionStatus
	Body            string
	FinalText       string
	AdvertiserName  string
	Erid            string
	MediaURLs       []string
	TokenCarrier    string
	TokenPosition   string
	RejectionReason string
	CreatedAt       time.Time
	// Links — адреса ссылок поста по порядку, с UTM-метками заказа (как ушли в канал).
	Links []string
	// UTMSource, UTMMedium — постоянные UTM-метки ссылок (A-21); utm_campaign — order_{номер}.
	UTMSource, UTMMedium string
}

// PostNotReadyError — черновик поста не готов к версии: коды проблем (R-6).
type PostNotReadyError struct{ Problems []string }

func (e *PostNotReadyError) Error() string { return "placement: post is not ready" }

// PostVersions — версии поста. Реализация — модуль post (CreateVersion в транзакции вызывающего).
type PostVersions interface {
	// Create — следующая версия из черновика кампании; черновик с проблемами — PostNotReadyError.
	Create(ctx context.Context, orgID, campaignID, userID uuid.UUID, orderNumber int64) (VersionInfo, error)
	// Get — версия с публичными ссылками изображений.
	Get(ctx context.Context, versionID uuid.UUID) (VersionInfo, error)
}

// HoldRequest — удержание под размещение (06 §2.3). Ключ идемпотентности будущего
// журнала: hold:{placement_id}:{post_version_id} (R-2).
type HoldRequest struct {
	PlacementID   uuid.UUID
	PostVersionID uuid.UUID
	Amount        money.Kopecks
}

// Holds — удержания на балансе рекламодателя (R-2, I-03). Вызывается в транзакции перехода
// (I-10). Реализация — модуль ledger (bootstrap/bridge.LedgerHolds).
type Holds interface {
	// HoldAll удерживает суммы атомарно; размещения с активным удержанием пропускаются.
	// Не хватает баланса — *InsufficientBalanceError с общей нехваткой, ничего не удержано.
	HoldAll(ctx context.Context, orgID uuid.UUID, reqs []HoldRequest) error
	// Release снимает остаток активного удержания размещения; без него ничего не делает.
	Release(ctx context.Context, placementID uuid.UUID, reason string) error
	// HasActive — у каких размещений есть активное удержание (R-7, проверка P-02).
	HasActive(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	// Settle распределяет удержание по Distribute (R-8): блогеру, комиссия, возврат. Вызывается в
	// транзакции P-12, P-13, P-15 с исходом accepted; by — актор проводки settlement (R-6 этапа приёмки).
	Settle(ctx context.Context, placementID, bloggerOrgID uuid.UUID, d Distribution, by Settler) error
}

// Settler — кто распределил удержание (актор проводки settlement, R-6 этапа приёмки): P-12 —
// рекламодатель, P-13 — система, P-15 — администратор.
type Settler struct {
	Actor  ActorType
	UserID *uuid.UUID
}

// ContractorGate — блогер может принять запрос (A-36, R-3): исполнитель Рокет Ворк active
// (юрлицо не проверяется). Реализация — модуль payout (SetPayoutPorts); до сборки — AllowAllContractors.
type ContractorGate interface {
	CanAccept(ctx context.Context, bloggerOrgID uuid.UUID) (bool, error)
}

// Notice — уведомление стороне размещения (R-4).
type Notice struct {
	Template       string
	OrganizationID uuid.UUID
	Role           ActorType
	ObjectType     string
	ObjectID       string
	// Key — ключ повтора (напоминание T-01): одно уведомление на ключ; пусто — без проверки.
	Key  string
	Data map[string]any
}

// Notifier — уведомления (R-4): вызывается в транзакции перехода и только ставит работу.
// Реализация — модуль notification (bootstrap/bridge.PlacementNotifier).
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}

// OutgoingPost — пост к отправке в канал (05 §2.6): итоговый текст и токены изображений по порядку.
type OutgoingPost struct {
	Text        string
	ImageTokens []string
	// Recover* — как найти свой пост при таймауте отправки, чтобы не опубликовать дважды
	// (03 §11, R-5): сообщение бота не раньше RecoverSince, текст содержит RecoverNeedle (erid).
	RecoverSenderID int64
	RecoverSince    time.Time
	RecoverNeedle   string
}

// ChannelPublisher — публикация и чтение постов канала в MAX (R-1). Реализация — integration/max
// через переходник в bootstrap. Вызывается только из обработчиков заданий вне транзакции (I-13).
// Ошибки, которые не повторяются (отказ 4xx), помечены job.Permanent; код отказа MAX —
// в *ExternalError.
type ChannelPublisher interface {
	// UploadImage загружает изображение и возвращает токен вложения. Токены не переиспользуются.
	UploadImage(ctx context.Context, chatID int64, r io.Reader, size int64, contentType string) (string, error)
	// SendPost публикует пост в канал.
	SendPost(ctx context.Context, chatID int64, post OutgoingPost) (SentMessage, error)
	// GetMessage читает сообщение; нет сообщения — ErrMessageNotFound.
	GetMessage(ctx context.Context, messageID string) (ChannelMessage, error)
	// DeleteMessage удаляет сообщение; успех проверяется по телу ответа.
	DeleteMessage(ctx context.Context, chatID int64, messageID string) error
	// ListMessages — сообщения канала за [from, to], новые первыми.
	ListMessages(ctx context.Context, chatID int64, from, to time.Time) ([]ChannelMessage, error)
	// FindPost ищет сообщение автора senderID за [from, to], текст которого содержит needle
	// (поиск своей публикации после неизвестного исхода, R-5); nil — не найдено.
	FindPost(ctx context.Context, chatID int64, from, to time.Time, senderID int64, needle string) (*ChannelMessage, error)
	// BotUserID — пользователь MAX бота платформы (R-8).
	BotUserID(ctx context.Context) (int64, error)
}

// ExternalError — отказ внешней системы с её кодом (для reason_text истории). Код — машинный,
// без персональных данных.
type ExternalError struct {
	Code string
	Err  error
}

func (e *ExternalError) Error() string { return "placement: external system rejected: " + e.Code }
func (e *ExternalError) Unwrap() error { return e.Err }

// PublicationContent — содержание версии поста для публикации (R-1). Реализация — модуль post.
type PublicationContent interface {
	// Get — версия с итоговым текстом, проверкой маркировки и изображениями по порядку.
	Get(ctx context.Context, versionID uuid.UUID) (Content, error)
	// Open открывает файл производной из закрытой области хранилища; закрывает вызывающий.
	Open(ctx context.Context, derivativeID uuid.UUID) (io.ReadCloser, error)
}

// LivePlacement — живая публикация канала вместе с размещением (контроль топа, R-10).
type LivePlacement struct {
	Placement   Placement
	Publication Publication
}

// Publications — публикации (04 §5).
type Publications interface {
	// Insert — новая публикация; у размещения уже есть — ErrPublicationExists.
	Insert(ctx context.Context, pub Publication) error
	// GetByPlacement — публикация размещения без блокировки; ErrNotFound, если нет.
	GetByPlacement(ctx context.Context, placementID uuid.UUID) (Publication, error)
	// GetByPlacementForUpdate — то же с блокировкой строки.
	GetByPlacementForUpdate(ctx context.Context, placementID uuid.UUID) (Publication, error)
	// GetForUpdate — публикация по идентификатору с блокировкой; ErrNotFound, если нет.
	GetForUpdate(ctx context.Context, id uuid.UUID) (Publication, error)
	// FindByMessageID — публикация по идентификатору сообщения MAX без блокировки.
	FindByMessageID(ctx context.Context, messageID string) (Publication, bool, error)
	// Update — снятие, намерение, removal_overdue, просмотры.
	Update(ctx context.Context, pub Publication) error
	// ListLive — живые публикации по id после after, не больше limit.
	ListLive(ctx context.Context, after uuid.UUID, limit int) ([]Publication, error)
	// ListLiveByChannel — живые публикации канала с размещениями в published или disputed.
	ListLiveByChannel(ctx context.Context, channelID uuid.UUID) ([]LivePlacement, error)
	// ListByPlacements — публикации размещений (ответы кабинетов).
	ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]Publication, error)
	// OverdueRemovals — публикации с removal_overdue, отмеченные раньше before (07 §3).
	OverdueRemovals(ctx context.Context, before time.Time) ([]uuid.UUID, error)
}

// Violations — нарушения (04 §5). Только добавление (R-20).
type Violations interface {
	// Insert — новое нарушение; повтор того же события — ErrDuplicateViolation.
	Insert(ctx context.Context, v Violation) error
	// ListByPlacement — нарушения размещения по времени (карточка спора).
	ListByPlacement(ctx context.Context, placementID uuid.UUID) ([]Violation, error)
}

// Disputes — споры (04 §5).
type Disputes interface {
	// Insert — новый спор; у размещения уже есть — ErrDisputeExists.
	Insert(ctx context.Context, d Dispute) error
	// GetByPlacement — спор размещения; ErrNotFound, если нет.
	GetByPlacement(ctx context.Context, placementID uuid.UUID) (Dispute, error)
	// ListByPlacements — споры размещений (ответы кабинетов).
	ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]Dispute, error)
	// GetByPlacementForUpdate — спор размещения с блокировкой; ErrNotFound, если нет.
	GetByPlacementForUpdate(ctx context.Context, placementID uuid.UUID) (Dispute, error)
	// Resolve сохраняет решение (S-18) один раз; решение уже есть — ErrDisputeResolved.
	Resolve(ctx context.Context, d Dispute) error
	// ListAdmin — споры для администратора по сроку T-06 (команда admin dispute list).
	ListAdmin(ctx context.Context, f DisputeFilter) ([]Dispute, error)
}

// DisputeFilter — выборка споров администратора: открытые (по умолчанию) или все, только
// просроченные к OverdueAt.
type DisputeFilter struct {
	All       bool
	OverdueAt *time.Time
	Limit     int
}

// Claims — претензии (04 §5). Только добавление (R-1 этапа приёмки).
type Claims interface {
	// Insert — новая претензия; по размещению уже есть — ErrClaimExists.
	Insert(ctx context.Context, c Claim) error
	// GetByPlacement — претензия размещения; ErrNotFound, если нет.
	GetByPlacement(ctx context.Context, placementID uuid.UUID) (Claim, error)
	// ListByPlacements — претензии размещений (ответы кабинетов).
	ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]Claim, error)
}

// ChannelRatings — рейтинг канала (A-08, R-13 этапа приёмки). Реализация — модуль каналов
// (bootstrap/bridge.PlacementChannelRatings), в транзакции вызывающего.
type ChannelRatings interface {
	// Lower уменьшает рейтинг на points, не ниже 0; возвращает прежнее и новое значения.
	Lower(ctx context.Context, channelID uuid.UUID, points int) (from, to int, err error)
}

// ScheduledRef — запланированное размещение и его время публикации.
type ScheduledRef struct {
	ID        uuid.UUID
	PublishAt time.Time
}

// CallPriority — приоритет вызовов ChannelPublisher для общего ограничителя MAX (05 §2.8):
// публикация и снятие ждут токен без предела, сверка уступает им (R-15).
type CallPriority int

const (
	// CallPublish — публикация и снятие.
	CallPublish CallPriority = iota + 1
	// CallReconcile — сверка и контроль.
	CallReconcile
)

type callPriorityKey struct{}

// WithCallPriority задаёт приоритет вызовов в context.
func WithCallPriority(ctx context.Context, p CallPriority) context.Context {
	return context.WithValue(ctx, callPriorityKey{}, p)
}

// CallPriorityFrom — приоритет вызовов из context; без него — сверка.
func CallPriorityFrom(ctx context.Context) CallPriority {
	if p, ok := ctx.Value(callPriorityKey{}).(CallPriority); ok {
		return p
	}
	return CallReconcile
}

// PayoutRequest — создать выплату блогеру по принятому размещению (R-4).
type PayoutRequest struct {
	PlacementID  uuid.UUID
	BloggerOrgID uuid.UUID
	Amount       money.Kopecks
}

// Payouts — создание выплаты (модуль payout, bootstrap/bridge.PlacementPayouts). Вызывается
// в транзакции перехода в accepted из P-12, P-13, P-15 на этапе «Приёмка, споры и возвраты»
// (I-10): строка payout и задание send_payout фиксируются вместе с переходом.
type Payouts interface {
	Create(ctx context.Context, r PayoutRequest) (uuid.UUID, error)
}
