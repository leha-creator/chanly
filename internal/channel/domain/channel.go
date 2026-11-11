// Package domain — правила модуля каналов (04 §3, 05 §2.4, S-12, S-25): канал и его
// состояние, права бота, коды подключения. Без базы, сети и журнала.
package domain

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Ошибки предметной области. Слой app решает, что ответить.
var (
	// ErrNotFound — записи нет.
	ErrNotFound = errors.New("channel: not found")
	// ErrAlreadyLinked — канал уже связан с этой же организацией.
	ErrAlreadyLinked = errors.New("channel: already linked to this organization")
	// ErrLinkedToOther — канал связан с другой организацией (R-10).
	ErrLinkedToOther = errors.New("channel: linked to another organization")
	// ErrNotLinkable — канал не ждёт кода подключения (бот покинул канал).
	ErrNotLinkable = errors.New("channel: not awaiting a link code")
	// ErrNotEditable — канал в состоянии, где блогер его не правит.
	ErrNotEditable = errors.New("channel: not editable in current status")
	// ErrStaleVersion — канал изменён параллельно (lock_version, 04 §1).
	ErrStaleVersion = errors.New("channel: stale lock_version")
)

// Status — состояние канала (04 §9).
type Status string

const (
	StatusUnclaimed     Status = "unclaimed"
	StatusPendingRights Status = "pending_rights"
	StatusModeration    Status = "moderation"
	StatusActive        Status = "active"
	StatusRejected      Status = "rejected"
	StatusSuspended     Status = "suspended"
	StatusDisconnected  Status = "disconnected"
	StatusBlocked       Status = "blocked"
)

// Statuses — все значения перечисления в порядке 04 §9.
var Statuses = []Status{
	StatusUnclaimed, StatusPendingRights, StatusModeration, StatusActive,
	StatusRejected, StatusSuspended, StatusDisconnected, StatusBlocked,
}

// RknStatus — состояние проверки номера в реестре РКН (04 §9, D-11).
type RknStatus string

const (
	RknNotRequired RknStatus = "not_required"
	RknPending     RknStatus = "pending"
	RknConfirmed   RknStatus = "confirmed"
	RknRejected    RknStatus = "rejected"
)

// Коды причины состояния (channel.status_reason, 04 §3). Для rejected причина —
// текст отказа модерации.
const (
	ReasonNotPublic        = "not_public"
	ReasonMissingRights    = "missing_rights"
	ReasonUnclaimedExpired = "unclaimed_expired"
	// ReasonRknRequired — подписчиков не меньше порога, а номер РКН не подтверждён (D-11, S-32 шаг 2).
	ReasonRknRequired = "rkn_required"
)

// resumable — причины приостановки, которые снимаются сами, когда факт исправлен:
// права, публичность и номер РКН (R-16). Остальные снимает администратор.
var resumable = []string{ReasonMissingRights, ReasonNotPublic, ReasonRknRequired}

// Права бота из MAX (05 §2.3).
const (
	PermissionWrite  = "write"
	PermissionDelete = "delete"
)

// RequiredPermissions — права, без которых канал не принимает размещения (A-01).
var RequiredPermissions = []string{PermissionWrite, PermissionDelete}

// Channel — канал MAX (04 §3). Пустая строка в текстовых полях — значения нет.
type Channel struct {
	ID               uuid.UUID
	OrganizationID   *uuid.UUID
	MaxChatID        int64
	Title            string
	MaxDescription   string
	IconURL          string
	Link             string
	IsPublic         bool
	AddedByMaxUserID int64
	Description      string
	CategoryID       *uuid.UUID
	Status           Status
	StatusReason     string
	// StatusBeforeDisconnect — состояние до удаления бота (R-9).
	StatusBeforeDisconnect *Status
	// UnclaimedSince — с какого момента канал ждёт кода подключения (R-12).
	UnclaimedSince          *time.Time
	BotIsAdmin              bool
	BotPermissions          []string
	BotPermissionsCheckedAt *time.Time
	SubscribersCount        int
	RknNumber               string
	RknStatus               RknStatus
	RknCheckedAt            *time.Time
	RknCheckedBy            *uuid.UUID
	Rating                  int
	ModeratedAt             *time.Time
	ModeratedBy             *uuid.UUID
	// OrdPlatformReadyAt — площадка канала зарегистрирована в ОРД: без неё канал не виден в
	// каталоге (этап «Маркировка», R-14). Ставит SetOrdPlatformReady.
	OrdPlatformReadyAt *time.Time
	// Prices — цены по форматам (channel_price), загружает репозиторий.
	Prices      []Price
	LockVersion int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ChatInfo — сведения о канале из MAX (GET /chats/{chatId}).
type ChatInfo struct {
	Title            string
	Description      string
	IconURL          string
	Link             string
	IsPublic         bool
	IsChannel        bool
	SubscribersCount int
}

// Membership — права бота в канале (GET /chats/{chatId}/members/me).
type Membership struct {
	IsOwner     bool
	IsAdmin     bool
	Permissions []string
}

// StatusChange — результат события: прежнее и новое состояние канала.
type StatusChange struct {
	ChannelID uuid.UUID
	From      Status
	To        Status
	Reason    string
}

// Changed сообщает, что состояние сменилось.
func (c StatusChange) Changed() bool { return c.From != c.To }

// AvailabilityLost — канал перестал принимать размещения (S-25 шаг 1).
func (c StatusChange) AvailabilityLost() bool {
	return c.From == StatusActive && c.To != StatusActive
}

// AvailabilityRestored — права вернулись, канал снова active без модерации (S-25 шаг 3).
func (c StatusChange) AvailabilityRestored() bool {
	return c.To == StatusActive && c.From == StatusSuspended
}

// NewChannel — канал после первого события bot_added: ждёт кода подключения (A-41).
func NewChannel(id uuid.UUID, maxChatID, addedBy int64, now time.Time) *Channel {
	return &Channel{
		ID:               id,
		MaxChatID:        maxChatID,
		AddedByMaxUserID: addedBy,
		Status:           StatusUnclaimed,
		UnclaimedSince:   &now,
		BotIsAdmin:       true,
		BotPermissions:   []string{},
		RknStatus:        RknNotRequired,
		Rating:           100,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// MissingPermissions — обязательные права, которых нет у бота (R-14).
func (c *Channel) MissingPermissions() []string {
	missing := []string{}
	for _, p := range RequiredPermissions {
		if !c.BotIsAdmin || !slices.Contains(c.BotPermissions, p) {
			missing = append(missing, p)
		}
	}
	return missing
}

// Approved — канал прошёл модерацию: при возврате прав он становится active
// без повторной проверки (05 §2.4 шаг 7).
func (c *Channel) Approved() bool {
	return c.ModeratedAt != nil && c.Status != StatusRejected
}

// VisibilityPolicy controls eligibility without changing the observed IsPublic
// fact. The default is strict; bootstrap enables private channels only in dev.
type VisibilityPolicy int

const (
	PublicChannelsOnly VisibilityPolicy = iota
	AllowPrivateChannels
)

// problem — почему канал не может принимать размещения: нет прав или канал закрытый.
func (c *Channel) problem(visibility ...VisibilityPolicy) string {
	if len(c.MissingPermissions()) > 0 {
		return ReasonMissingRights
	}
	if !c.IsPublic && (len(visibility) == 0 || visibility[0] != AllowPrivateChannels) {
		return ReasonNotPublic
	}
	return ""
}

// RknRequired — подписчиков не меньше порога, а номер РКН не подтверждён: реклама
// в канале запрещена (D-11).
func (c *Channel) RknRequired(rknThreshold int) bool {
	return c.SubscribersCount >= rknThreshold && c.RknStatus != RknConfirmed
}

// target — состояние, которое следует из фактов канала (R-8 этапа «Подключение
// каналов», R-16): права и публичность, затем номер РКН. rknThreshold — порог
// подписчиков RKN_SUBSCRIBERS_THRESHOLD.
func (c *Channel) target(rknThreshold int, visibility ...VisibilityPolicy) (Status, string) {
	switch c.Status {
	case StatusBlocked, StatusDisconnected, StatusRejected:
		return c.Status, c.StatusReason
	case StatusUnclaimed:
		return StatusUnclaimed, ""
	}
	if reason := c.problem(visibility...); reason != "" {
		if c.Status == StatusActive || c.Status == StatusSuspended {
			return StatusSuspended, reason
		}
		return StatusPendingRights, reason
	}
	switch c.Status {
	case StatusPendingRights:
		return StatusModeration, ""
	case StatusActive, StatusSuspended:
		if c.RknRequired(rknThreshold) {
			return StatusSuspended, ReasonRknRequired
		}
		// Возвращаем только приостановку, причина которой снята; другие причины
		// снимает администратор.
		if c.Status == StatusSuspended && !slices.Contains(resumable, c.StatusReason) {
			return StatusSuspended, c.StatusReason
		}
		return StatusActive, ""
	}
	return c.Status, ""
}

// resolve приводит состояние к фактам и отмечает изменение.
func (c *Channel) resolve(rknThreshold int, now time.Time, visibility ...VisibilityPolicy) StatusChange {
	change := StatusChange{ChannelID: c.ID, From: c.Status}
	c.Status, c.StatusReason = c.target(rknThreshold, visibility...)
	change.To, change.Reason = c.Status, c.StatusReason
	c.touch(now)
	return change
}

func (c *Channel) touch(now time.Time) {
	c.UpdatedAt = now
}

// BotAdded — бот снова добавлен в известный канал (05 §2.4 шаг 8, R-9).
// Права до чтения из MAX считаются отсутствующими; refresh_channel_info
// переведёт канал дальше.
func (c *Channel) BotAdded(addedBy int64, now time.Time) StatusChange {
	switch c.Status {
	case StatusDisconnected:
		prev := c.StatusBeforeDisconnect
		c.StatusBeforeDisconnect = nil
		c.AddedByMaxUserID = addedBy
		c.BotIsAdmin, c.BotPermissions = false, []string{}
		switch {
		case c.OrganizationID == nil:
			c.Status, c.StatusReason, c.UnclaimedSince = StatusUnclaimed, "", &now
		case prev != nil && (*prev == StatusActive || *prev == StatusSuspended):
			c.Status, c.StatusReason = StatusSuspended, ReasonMissingRights
		default:
			c.Status, c.StatusReason = StatusPendingRights, ReasonMissingRights
		}
		change := StatusChange{ChannelID: c.ID, From: StatusDisconnected, To: c.Status, Reason: c.StatusReason}
		c.touch(now)
		return change
	case StatusUnclaimed:
		c.AddedByMaxUserID = addedBy
		c.touch(now)
	}
	return StatusChange{ChannelID: c.ID, From: c.Status, To: c.Status, Reason: c.StatusReason}
}

// BotRemoved — бота удалили из канала (S-25 шаг 1).
func (c *Channel) BotRemoved(now time.Time) StatusChange {
	from := c.Status
	if c.Status == StatusDisconnected {
		return StatusChange{ChannelID: c.ID, From: from, To: from, Reason: c.StatusReason}
	}
	c.BotIsAdmin, c.BotPermissions = false, []string{}
	if c.Status != StatusBlocked {
		prev := c.Status
		c.StatusBeforeDisconnect = &prev
		c.Status, c.StatusReason, c.UnclaimedSince = StatusDisconnected, "", nil
	}
	c.touch(now)
	return StatusChange{ChannelID: c.ID, From: from, To: c.Status, Reason: c.StatusReason}
}

// RightsRefreshed — прочитаны сведения о канале и права бота (05 §2.4 шаги 4–6).
// Число подписчиков применяет правило РКН (R-16).
func (c *Channel) RightsRefreshed(info ChatInfo, m Membership, rknThreshold int, now time.Time, visibility ...VisibilityPolicy) StatusChange {
	c.applyChatInfo(info, rknThreshold)
	c.BotIsAdmin = m.IsAdmin
	c.BotPermissions = slices.Clone(m.Permissions)
	if c.BotPermissions == nil {
		c.BotPermissions = []string{}
	}
	c.BotPermissionsCheckedAt = &now
	return c.resolve(rknThreshold, now, visibility...)
}

// MetricsRefreshed — сведения о канале из суточного задания метрик (S-32): название,
// описание, публичность и подписчики; затем правило РКН (R-16).
func (c *Channel) MetricsRefreshed(info ChatInfo, rknThreshold int, now time.Time, visibility ...VisibilityPolicy) StatusChange {
	c.applyChatInfo(info, rknThreshold)
	return c.resolve(rknThreshold, now, visibility...)
}

// applyChatInfo переносит сведения MAX в канал и пересчитывает, нужен ли номер РКН.
func (c *Channel) applyChatInfo(info ChatInfo, rknThreshold int) {
	c.Title, c.MaxDescription, c.IconURL, c.Link = info.Title, info.Description, info.IconURL, info.Link
	c.IsPublic = info.IsPublic
	c.SubscribersCount = max(info.SubscribersCount, 0)
	c.RknStatus = nextRknStatus(c.RknStatus, false, c.SubscribersCount, rknThreshold)
}

// TitleChanged — событие chat_title_changed.
func (c *Channel) TitleChanged(title string, now time.Time) {
	c.Title = title
	c.touch(now)
}

// LinkTo связывает канал с организацией кода подключения (05 §2.4 шаг 3).
func (c *Channel) LinkTo(orgID uuid.UUID, rknThreshold int, now time.Time, visibility ...VisibilityPolicy) (StatusChange, error) {
	if c.OrganizationID != nil {
		if *c.OrganizationID == orgID {
			return StatusChange{}, ErrAlreadyLinked
		}
		return StatusChange{}, ErrLinkedToOther
	}
	if c.Status != StatusUnclaimed {
		return StatusChange{}, ErrNotLinkable
	}
	c.OrganizationID = &orgID
	c.UnclaimedSince = nil
	c.Status = StatusPendingRights
	change := c.resolve(rknThreshold, now, visibility...)
	change.From = StatusUnclaimed
	return change, nil
}

// ReleaseUnclaimed — канал не подключён кодом за CHANNEL_UNCLAIMED_TTL, бот уходит (R-12).
func (c *Channel) ReleaseUnclaimed(now time.Time) (StatusChange, bool) {
	if c.Status != StatusUnclaimed {
		return StatusChange{}, false
	}
	prev := c.Status
	c.StatusBeforeDisconnect = &prev
	c.Status, c.StatusReason, c.UnclaimedSince = StatusDisconnected, ReasonUnclaimedExpired, nil
	c.BotIsAdmin, c.BotPermissions = false, []string{}
	c.touch(now)
	return StatusChange{ChannelID: c.ID, From: prev, To: c.Status, Reason: c.StatusReason}, true
}

// ProfileUpdate — правка блогером (S-12 шаг 5). nil — поле не меняется.
type ProfileUpdate struct {
	Description *string
	CategoryID  *uuid.UUID
	RknNumber   *string
}

// UpdateProfile применяет правку блогера. rknThreshold — порог подписчиков, с
// которого нужен номер в реестре РКН (D-11, 07 §8).
func (c *Channel) UpdateProfile(p ProfileUpdate, rknThreshold int, now time.Time, visibility ...VisibilityPolicy) (StatusChange, error) {
	switch c.Status {
	case StatusUnclaimed, StatusDisconnected, StatusBlocked:
		return StatusChange{}, ErrNotEditable
	}
	if p.Description != nil {
		c.Description = *p.Description
	}
	if p.CategoryID != nil {
		id := *p.CategoryID
		c.CategoryID = &id
	}
	numberChanged := p.RknNumber != nil && *p.RknNumber != c.RknNumber
	if p.RknNumber != nil {
		c.RknNumber = *p.RknNumber
	}
	c.RknStatus = nextRknStatus(c.RknStatus, numberChanged, c.SubscribersCount, rknThreshold)
	if numberChanged {
		c.RknCheckedAt, c.RknCheckedBy = nil, nil
	}
	// Канал из pending_rights или rejected после правки уходит на модерацию, если права
	// в порядке (операция updateBloggerChannel).
	from := c.Status
	if c.Status == StatusRejected {
		c.Status, c.StatusReason = StatusPendingRights, ""
	}
	change := c.resolve(rknThreshold, now, visibility...)
	change.From = from
	return change, nil
}

// nextRknStatus — состояние проверки номера РКН после правки (D-11).
func nextRknStatus(cur RknStatus, numberChanged bool, subscribers, threshold int) RknStatus {
	if subscribers < threshold {
		if cur == RknConfirmed && !numberChanged {
			return cur
		}
		return RknNotRequired
	}
	if numberChanged || cur == RknNotRequired {
		return RknPending
	}
	return cur
}

// Шаблоны уведомлений блогеру о канале (каталог модуля notification).
const (
	NoticeApproved  = "channel.approved"
	NoticeRejected  = "channel.rejected"
	NoticeSuspended = "channel.suspended"
	// NoticeBlocked — канал заблокирован администратором (R-7 этапа «API администратора»).
	NoticeBlocked      = "channel.blocked"
	NoticeDisconnected = "channel.disconnected"
	NoticeRestored     = "channel.restored"
)

// NoticeTemplate — шаблон уведомления блогеру о смене состояния; false — уведомлять не о чем.
// Отключение непривязанного канала (unclaimed_expired) — без уведомления: у канала нет
// организации.
func (c StatusChange) NoticeTemplate() (string, bool) {
	if !c.Changed() {
		return "", false
	}
	switch c.To {
	case StatusActive:
		switch c.From {
		case StatusModeration:
			return NoticeApproved, true
		case StatusSuspended:
			return NoticeRestored, true
		}
	case StatusRejected:
		return NoticeRejected, true
	case StatusBlocked:
		return NoticeBlocked, true
	case StatusSuspended:
		if c.From == StatusActive {
			return NoticeSuspended, true
		}
	case StatusDisconnected:
		if c.Reason != ReasonUnclaimedExpired {
			return NoticeDisconnected, true
		}
	}
	return "", false
}
