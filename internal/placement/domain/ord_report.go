package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrdReportRow — размещение с публикацией для ежемесячной отчётности ОРД (этап «API администратора
// и отчётность ОРД», R-19, R-20). Читает модуль маркировки через bootstrap/bridge.
type OrdReportRow struct {
	FeeAmount                int64
	FeeCharged               *int64
	PlacementID              uuid.UUID
	PublicationID            uuid.UUID
	ChannelID                uuid.UUID
	PostVersionID            uuid.UUID
	AdvertiserOrganizationID uuid.UUID
	BloggerOrganizationID    uuid.UUID
	PublishedAt              time.Time
	// RemovedAt — nil: публикация в канале.
	RemovedAt *time.Time
	// Views — последняя сверка публикации; FinalViews — итог размещения.
	Views      *int64
	FinalViews *int64
	// TotalAmount — сумма рекламодателя; RefundAmount — возврат (nil — не определён).
	TotalAmount  int64
	RefundAmount *int64
	// BloggerAmount — вознаграждение по заказу; PayoutAmount — к выплате после итога (nil — не определено).
	BloggerAmount int64
	PayoutAmount  *int64
}

// OrdLiveViews — показы живой публикации на момент снимка.
type OrdLiveViews struct {
	PublicationID uuid.UUID
	Views         int64
	CheckedAt     *time.Time
}
