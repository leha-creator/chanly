package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// TimeOfDay — время суток по Москве в минутах от полуночи (A-39).
type TimeOfDay int

// ParseTimeOfDay разбирает `ЧЧ:ММ`.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("time of day %q: %w", s, err)
	}
	return TimeOfDay(t.Hour()*60 + t.Minute()), nil
}

// String — `ЧЧ:ММ`.
func (t TimeOfDay) String() string { return fmt.Sprintf("%02d:%02d", int(t)/60, int(t)%60) }

// Duration — смещение от полуночи.
func (t TimeOfDay) Duration() time.Duration { return time.Duration(t) * time.Minute }

// Placement — размещение: один канал заказа, единица работы (03 §1, 04 §5, D-02).
// Состояние меняет только Apply.
type Placement struct {
	ID        uuid.UUID
	OrderID   uuid.UUID
	ChannelID uuid.UUID
	// BloggerOrgID — снимок владельца канала при создании.
	BloggerOrgID uuid.UUID
	// Снимок формата (03 §2).
	FormatID      string
	TopHours      int
	DurationHours int
	// Снимок периода кампании (R-9).
	WindowStart time.Time
	WindowEnd   time.Time
	TimeFrom    TimeOfDay
	TimeTo      TimeOfDay
	// PostVersionID — версия, с которой размещение выйдет или вышло.
	PostVersionID   uuid.UUID
	Status          Status
	StatusChangedAt time.Time
	StatusReason    string
	// SentToBloggerAt — первый P-02: с него размещение видно блогеру (R-16).
	SentToBloggerAt *time.Time
	FeeRateBP       int
	// Суммы неизменяемы (I-05).
	BloggerAmount      money.Kopecks
	FeeAmount          money.Kopecks
	TotalAmount        money.Kopecks
	AcceptDeadlineAt   *time.Time
	ScheduleDeadlineAt *time.Time
	PublishAt          *time.Time
	RemoveAt           *time.Time
	PublishedAt        *time.Time
	RemovedAt          *time.Time
	ClaimDeadlineAt    *time.Time
	PerformanceShare   *int
	PayoutAmount       *money.Kopecks
	FeeCharged         *money.Kopecks
	RefundAmount       *money.Kopecks
	FinalViews         *int
	LockVersion        int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Window — снимок окна размещения.
func (p *Placement) Window() Window {
	return Window{Start: p.WindowStart, End: p.WindowEnd, TimeFrom: p.TimeFrom, TimeTo: p.TimeTo}
}

// Format — снимок формата размещения.
func (p *Placement) Format() Format {
	return Format{ID: p.FormatID, TopHours: p.TopHours, DurationHours: p.DurationHours}
}

// Job — задание, которое переход ставит в той же транзакции (I-10, I-13). Значение,
// а не вызов: app переводит его в platform/job.
type Job struct {
	Kind      string
	DedupeKey string
	RunAt     time.Time
	Payload   any
}

// JobMove — перенос ожидающего задания по dedupe_key (P-07).
type JobMove struct {
	DedupeKey string
	RunAt     time.Time
}

// Шаблоны уведомлений (R-4).
const (
	NoticeRequest      = "placement.request"
	NoticeAccepted     = "placement.accepted"
	NoticeDeclined     = "placement.declined"
	NoticeExpired      = "placement.expired"
	NoticeCancelled    = "placement.cancelled"
	NoticeScheduled    = "placement.scheduled"
	NoticeRescheduled  = "placement.rescheduled"
	NoticeReturned     = "placement.returned"
	NoticePostRejected = "order.post_rejected"
	// NoticeErirRejected — ЕРИР отклонил креатив заказа (этап «Маркировка», R-17).
	NoticeErirRejected = "order.erir_rejected"
	// Этап «Автопостинг и контроль» (R-19).
	NoticePublished      = "placement.published"
	NoticeFailed         = "placement.failed"
	NoticeRemoved        = "placement.removed"
	NoticeRemovalOverdue = "placement.removal_overdue"
	NoticeDisputed       = "placement.disputed"
	// NoticeDisputeResolved — решение по спору (P-15, этап «Приёмка, споры и возвраты»).
	NoticeDisputeResolved = "placement.dispute_resolved"
	// NoticeDisputeMessage — новое сообщение по спору (R-9 этапа «API администратора»).
	NoticeDisputeMessage = "placement.dispute_message"
	// NoticeAcceptReminder — до T-01 осталось ReminderLead (R-8 этапа уведомлений).
	NoticeAcceptReminder = "placement.accept_reminder"
	// NoticePostApproved — модератор одобрил версию поста заказа.
	NoticePostApproved = "order.post_approved"
)

// NoticeTo — уведомление стороне размещения; организацию подставляет app.
type NoticeTo struct {
	Template string
	Role     ActorType
	// Key — ключ повтора уведомления; пусто — без проверки.
	Key string
	// Data — данные перехода для текста поверх данных размещения (адрес публикации).
	Data map[string]any
}

// StateChange — результат перехода: запись истории, задания и уведомления, которые app
// сохраняет в той же транзакции (I-10).
type StateChange struct {
	PlacementID uuid.UUID
	OrderID     uuid.UUID
	Code        TransitionCode
	From        Status
	To          Status
	Actor       ActorType
	UserID      *uuid.UUID
	Reason      string
	// ReasonText — текст причины (комментарий блогера при отказе). В журнал не пишется.
	ReasonText string
	At         time.Time
	// PrevLockVersion — lock_version до перехода: условие обновления строки (03 §11).
	PrevLockVersion int
	Jobs            []Job
	CancelJobs      []string
	MoveJobs        []JobMove
	Notices         []NoticeTo
	// ReleaseHold — удержание снимается (06 §2.3): конечное состояние, кроме paid.
	ReleaseHold bool
}

// ErrStaleVersion — размещение изменено параллельно: строка не обновилась (03 §11).
var ErrStaleVersion = errors.New("placement: stale status or lock_version")

// ErrNotFound — записи нет или она чужая; app переводит в 404.
var ErrNotFound = errors.New("placement: not found")
