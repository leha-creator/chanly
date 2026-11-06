package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Виды объектов ежемесячной отчётности (R-20, R-22).
const (
	TypeStatistics       ObjectType = "statistics"
	TypeInvoice          ObjectType = "invoice"
	TypeInvoiceCreatives ObjectType = "invoice_creatives"
)

// Statistics — строка статистики месяца в ОРД (ord_statistics). В ОРД идентифицируется id записи.
type Statistics struct {
	Finance     ReportFinance
	ID          uuid.UUID
	CreativeID  uuid.UUID
	PlatformID  uuid.UUID
	PlacementID uuid.UUID
	Period      Period
	Impressions int64
	ShownFrom   time.Time
	ShownTo     time.Time
	Amount      int64
	Sync        SyncState
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Ref — ссылка для заданий.
func (s Statistics) Ref() ObjectRef { return ObjectRef{Type: TypeStatistics, ID: s.ID} }

// ExternalID — идентификатор в ОРД.
func (s Statistics) ExternalID() string { return s.ID.String() }

// SetLine переносит строку пересчёта; true — данные изменились (запись снова ждёт отправки, R-21).
func (s *Statistics) SetLine(l StatisticsLine) bool {
	changed := s.Finance != l.Finance || s.Impressions != l.Impressions || s.Amount != l.Amount || !s.ShownFrom.Equal(l.ShownFrom) || !s.ShownTo.Equal(l.ShownTo)
	s.Finance = l.Finance
	s.Impressions, s.Amount, s.ShownFrom, s.ShownTo = l.Impressions, l.Amount, l.ShownFrom, l.ShownTo
	return changed
}

// StatisticsRequest — тело POST /statistics.
// ПРЕДПОЛОЖЕНИЕ (Q-42): план показов равен факту, даты — YYYY-MM-DD, сумма — рубли строкой.
type StatisticsRequest struct {
	Finance       ReportFinance
	ID            string `json:"id"`
	CreativeID    string `json:"creativeId"`
	PlatformID    string `json:"platformId"`
	ImpsFact      int64  `json:"impsFact"`
	ImpsPlan      int64  `json:"impsPlan"`
	StartDateFact string `json:"startDateFact"`
	EndDateFact   string `json:"endDateFact"`
	StartDatePlan string `json:"startDatePlan"`
	EndDatePlan   string `json:"endDatePlan"`
	Amount        string `json:"amount"`
}

// NewStatisticsRequest собирает запрос по строке и внешним идентификаторам креатива и площадки.
func NewStatisticsRequest(s Statistics, creativeExternalID, platformExternalID string) StatisticsRequest {
	from, to := s.ShownFrom.Format(time.DateOnly), s.ShownTo.Format(time.DateOnly)
	return StatisticsRequest{
		Finance: s.Finance,
		ID:      s.ExternalID(), CreativeID: creativeExternalID, PlatformID: platformExternalID,
		ImpsFact: s.Impressions, ImpsPlan: s.Impressions, StartDateFact: from, EndDateFact: to,
		StartDatePlan: from, EndDatePlan: to, Amount: Rubles(s.Amount),
	}
}

// Rubles — копейки рублями строкой с точкой: 345000 → "3450.00".
func Rubles(kopecks int64) string { return fmt.Sprintf("%d.%02d", kopecks/100, kopecks%100) }

// Invoice — акт месяца по договору в ОРД (ord_invoice).
type Invoice struct {
	Finance     ReportFinance
	Links       SyncState
	ID          uuid.UUID
	ContractID  uuid.UUID
	Period      Period
	Type        string
	Number      string
	InvoiceDate time.Time
	Amount      int64
	Items       []InvoiceItem
	Sync        SyncState
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Ref — ссылка для заданий.
func (i Invoice) Ref() ObjectRef { return ObjectRef{Type: TypeInvoice, ID: i.ID} }

func (i Invoice) LinksRef() ObjectRef { return ObjectRef{Type: TypeInvoiceCreatives, ID: i.ID} }

// ExternalID — идентификатор в ОРД.
func (i Invoice) ExternalID() string { return i.ID.String() }

// InvoiceRequest is the persisted financial input shared by independent document and links submissions.
type InvoiceRequest struct {
	Finance        ReportFinance
	ClientRole     string
	ContractorRole string
	ID             string               `json:"id"`
	ContractID     string               `json:"contractId"`
	Type           string               `json:"type"`
	Number         string               `json:"number"`
	Date           string               `json:"date"`
	StartDate      string               `json:"startDate"`
	EndDate        string               `json:"endDate"`
	Amount         string               `json:"amount"`
	Items          []InvoiceRequestItem `json:"items"`
}

// InvoiceRequestItem — строка разбивки в запросе.
type InvoiceRequestItem struct {
	ContractID string
	Tax        TaxTotal
	CreativeID string `json:"creativeId"`
	PlatformID string `json:"platformId"`
	Amount     string `json:"amount"`
}

// NewInvoiceRequest собирает запрос акта; items — строки с внешними идентификаторами.
func NewInvoiceRequest(i Invoice, contractExternalID string, items []InvoiceRequestItem) InvoiceRequest {
	return InvoiceRequest{
		Finance: i.Finance,
		ID:      i.ExternalID(), ContractID: contractExternalID, Type: i.Type, Number: i.Number,
		Date: i.InvoiceDate.Format(time.DateOnly), StartDate: i.Period.Date().Format(time.DateOnly),
		EndDate: i.Period.LastDay().Format(time.DateOnly), Amount: Rubles(i.Amount), Items: items,
	}
}
