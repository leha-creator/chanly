package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Period — календарный месяц по Москве (R-16).
type Period struct {
	year  int
	month time.Month
}

// PeriodOf — месяц, в который попадает момент t по Москве.
func PeriodOf(t time.Time) Period {
	m := t.In(msk)
	return Period{year: m.Year(), month: m.Month()}
}

// PreviousPeriod — прошедший месяц относительно now (задание 1-го числа).
func PreviousPeriod(now time.Time) Period {
	start := PeriodOf(now).Start()
	return PeriodOf(start.Add(-time.Nanosecond))
}

// ParsePeriod — период из строки YYYY-MM.
func ParsePeriod(s string) (Period, error) {
	t, err := time.ParseInLocation("2006-01", s, msk)
	if err != nil {
		return Period{}, fmt.Errorf("billing: period %q: want YYYY-MM", s)
	}
	return PeriodOf(t), nil
}

// PeriodFromDate — период по дате первого числа (closing_act.period).
func PeriodFromDate(d time.Time) Period { return Period{year: d.Year(), month: d.Month()} }

// Start — 00:00 МСК первого числа.
func (p Period) Start() time.Time { return time.Date(p.year, p.month, 1, 0, 0, 0, 0, msk) }

// End — 00:00 МСК первого числа следующего месяца; период полуоткрытый.
func (p Period) End() time.Time { return p.Start().AddDate(0, 1, 0) }

// Date — первое число месяца как дата (UTC, без времени) для closing_act.period.
func (p Period) Date() time.Time { return time.Date(p.year, p.month, 1, 0, 0, 0, 0, time.UTC) }

// String — YYYY-MM.
func (p Period) String() string { return fmt.Sprintf("%04d-%02d", p.year, int(p.month)) }

// IsZero — период не задан.
func (p Period) IsZero() bool { return p.year == 0 }

// FormatActNumber — номер закрывающих документов «А-» и шесть цифр (R-16).
func FormatActNumber(seq int64) string { return fmt.Sprintf("А-%06d", seq) }

// ReportLine — строка отчёта агента: размещение с распределением удержания в периоде (06 §2.5).
type ReportLine struct {
	PlacementID  uuid.UUID
	OrderNumber  int64
	ChannelTitle string
	PublishedAt  *time.Time
	RemovedAt    *time.Time
	Payout       money.Kopecks
	Fee          money.Kopecks
	SettledAt    time.Time
}

// ReportTotals — итоги отчёта: услуги блогеров и вознаграждение платформы.
func ReportTotals(lines []ReportLine) (placements, fee money.Kopecks) {
	for _, l := range lines {
		placements += l.Payout
		fee += l.Fee
	}
	return placements, fee
}

// ClosingAct — отчёт агента и акт организации за месяц (A-20).
type ClosingAct struct {
	ID               uuid.UUID
	OrganizationID   uuid.UUID
	Period           Period
	Number           string
	PlacementsAmount money.Kopecks
	FeeAmount        money.Kopecks
	ReportKey        string
	ActKey           string
	CreatedAt        time.Time
}

// ClosingDocument — один из двух PDF закрывающих документов (R-16, R-19).
type ClosingDocument string

const (
	DocumentReport ClosingDocument = "report"
	DocumentAct    ClosingDocument = "act"
)
