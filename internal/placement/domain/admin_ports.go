package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Порты разделов администратора (этап «API администратора»).

// AdminOrderFilter — очередь модерации поста (S-17): состояние (nil — все), номер или ИНН.
type AdminOrderFilter struct {
	Status *OrderStatus
	Query  string
	// ByDeadline — сначала по началу периода публикации (moderation).
	ByDeadline    bool
	Offset, Limit int
}

// AdminOrderRow — строка очереди модерации.
type AdminOrderRow struct {
	ID              uuid.UUID
	Number          int64
	OrganizationID  uuid.UUID
	Status          OrderStatus
	TotalAmount     money.Kopecks
	CreatedAt       time.Time
	CampaignName    string
	PublishFrom     time.Time
	PlacementsCount int
	WaitingSince    time.Time
}

// AdminPlacementFilter — размещения администратора (R-5).
type AdminPlacementFilter struct {
	Status         *Status
	ChannelID      *uuid.UUID
	OrderID        *uuid.UUID
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	RemovalOverdue bool
	Now            time.Time
	Offset, Limit  int
}

// AdminPlacementRow — строка списка размещений администратора.
type AdminPlacementRow struct {
	Placement      Placement
	OrderNumber    int64
	AdvertiserID   uuid.UUID
	RemovalOverdue bool
}

// DisputeState — выборка споров: open, resolved, all.
type DisputeState string

const (
	DisputesOpen     DisputeState = "open"
	DisputesResolved DisputeState = "resolved"
	DisputesAll      DisputeState = "all"
)

// AdminDisputeListFilter — споры администратора (R-10).
type AdminDisputeListFilter struct {
	State         DisputeState
	OverdueAt     *time.Time
	Offset, Limit int
}

// AdminDisputeListRow — строка списка споров с размещением и заказом.
type AdminDisputeListRow struct {
	Dispute         Dispute
	PlacementStatus Status
	TotalAmount     money.Kopecks
	ChannelID       uuid.UUID
	BloggerOrgID    uuid.UUID
	OrderNumber     int64
	AdvertiserID    uuid.UUID
}

// AdminCounts — счётчики модуля для разделов администратора (R-16).
type AdminCounts struct {
	OrdersModeration int
	DisputesOpen     int
	DisputesOverdue  int
	RemovalsOverdue  int
}

// AdminReads — выборки администратора с общим числом строк.
type AdminReads interface {
	Orders(ctx context.Context, f AdminOrderFilter) ([]AdminOrderRow, int, error)
	Placements(ctx context.Context, f AdminPlacementFilter) ([]AdminPlacementRow, int, error)
	Disputes(ctx context.Context, f AdminDisputeListFilter) ([]AdminDisputeListRow, int, error)
	// DisputeByID — спор по идентификатору; ErrNotFound, если нет.
	DisputeByID(ctx context.Context, id uuid.UUID) (Dispute, error)
	Counts(ctx context.Context, now time.Time) (AdminCounts, error)
}

// DisputeMessages — переписка по спору (R-9). Только добавление.
type DisputeMessages interface {
	Insert(ctx context.Context, m DisputeMessage) error
	List(ctx context.Context, disputeID uuid.UUID) ([]DisputeMessage, error)
}

// PostModeration — решение администратора по версии поста (S-17). Реализация — модуль post
// (bootstrap/bridge.PlacementPostModeration); заказ узнаёт о смене через слушатель версии.
type PostModeration interface {
	Approve(ctx context.Context, versionID, adminID uuid.UUID) error
	Reject(ctx context.Context, versionID, adminID uuid.UUID, reason string) error
	// MarkingDisabled — маркировка выключена (ORD_ENABLED=false, только разработка, I-2):
	// одобренная версия сразу получает erid разработки.
	MarkingDisabled() bool
	MarkReady(ctx context.Context, versionID uuid.UUID, erid string) error
}
