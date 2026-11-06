package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Ежемесячная отчётность ОРД (этап «API администратора и отчётность ОРД», R-18 … R-20, 05 §3.7):
// показы и суммы месяца по парам «креатив + площадка», акты по договорам с разбивкой.

// Period — отчётный месяц по Москве.
type Period struct {
	Year  int
	Month time.Month
}

// ReasonPeriodNotClosed — формировать и отправлять можно только прошедший месяц.
const ReasonPeriodNotClosed = "period_not_closed"

// ParsePeriod разбирает `YYYY-MM`.
func ParsePeriod(s string) (Period, error) {
	t, err := time.ParseInLocation("2006-01", s, msk)
	if err != nil || t.Format("2006-01") != s {
		return Period{}, fmt.Errorf("period %q: want YYYY-MM", s)
	}
	return Period{Year: t.Year(), Month: t.Month()}, nil
}

// PeriodOf — месяц момента t по Москве.
func PeriodOf(t time.Time) Period {
	m := t.In(msk)
	return Period{Year: m.Year(), Month: m.Month()}
}

// PeriodFromDate — месяц по дате из базы (первое число, полночь UTC).
func PeriodFromDate(d time.Time) Period { return Period{Year: d.Year(), Month: d.Month()} }

func (p Period) String() string { return fmt.Sprintf("%04d-%02d", p.Year, int(p.Month)) }

// Start — начало месяца по Москве.
func (p Period) Start() time.Time { return time.Date(p.Year, p.Month, 1, 0, 0, 0, 0, msk) }

// End — начало следующего месяца по Москве (не включается).
func (p Period) End() time.Time { return p.Start().AddDate(0, 1, 0) }

// Next — следующий месяц.
func (p Period) Next() Period { return PeriodOf(p.End()) }

// Prev — предыдущий месяц.
func (p Period) Prev() Period { return PeriodOf(p.Start().Add(-time.Hour)) }

// Date — первое число как дата базы (полночь UTC).
func (p Period) Date() time.Time { return time.Date(p.Year, p.Month, 1, 0, 0, 0, 0, time.UTC) }

// LastDay — последний день месяца (дата акта, Q-43).
func (p Period) LastDay() time.Time { return p.Date().AddDate(0, 1, -1) }

// DueDate — срок сдачи: 30-е число следующего месяца, в феврале — последний день (05 §3.7).
func (p Period) DueDate() time.Time {
	n := p.Next()
	last := n.LastDay()
	if last.Day() < 30 {
		return last
	}
	return time.Date(n.Year, n.Month, 30, 0, 0, 0, 0, time.UTC)
}

// Closed — месяц закончился к моменту now.
func (p Period) Closed(now time.Time) bool { return !now.Before(p.End()) }

// ReportPlacement — размещение с публикацией, пересекающей месяц (вход правил R-19, R-20).
type ReportPlacement struct {
	Finance      ReportFinance
	PublisherTax TaxTotal
	PlacementID  uuid.UUID
	CreativeID   uuid.UUID
	PlatformID   uuid.UUID
	// InitialContractID, PublisherContractID — договоры с рекламодателем и с блогером.
	InitialContractID   uuid.UUID
	PublisherContractID uuid.UUID
	PublishedAt         time.Time
	// RemovedAt — нулевое: публикация ещё в канале.
	RemovedAt time.Time
	// FinalViews — итог показов снятой публикации (placement.final_views или publication.views).
	FinalViews int64
	// ViewsAtStart, ViewsAtEnd — снимки на начало месяца и следующего; HasStart, HasEnd — снимок есть.
	ViewsAtStart int64
	HasStart     bool
	ViewsAtEnd   int64
	HasEnd       bool
	// AdvertiserAmount — сумма рекламодателя за вычетом возврата (Q-44: неизвестный возврат — 0).
	AdvertiserAmount int64
	// BloggerAmount — вознаграждение блогера.
	BloggerAmount int64
}

// StatisticsLine — строка статистики месяца по паре «креатив + площадка».
type StatisticsLine struct {
	Finance     ReportFinance
	CreativeID  uuid.UUID
	PlatformID  uuid.UUID
	PlacementID uuid.UUID
	Impressions int64
	ShownFrom   time.Time
	ShownTo     time.Time
	// Amount — сумма рекламодателя по размещениям пары, закончившимся в этом месяце.
	Amount int64
	// Placements — размещения пары (для разбивки актов).
	Placements []ReportPlacement
}

// BuildStatistics — строки месяца (R-19, R-20). Показы — разница снимков начала и конца месяца;
// публикация этого месяца начинает с 0, снятая в месяце заканчивает итогом; отрицательная разница —
// 0. Сумма — только в месяце окончания размещения. Даты показа — по Москве.
func BuildStatistics(placements []ReportPlacement, period Period) []StatisticsLine {
	start, end := period.Start(), period.End()
	byPair := map[[2]uuid.UUID]*StatisticsLine{}
	var order [][2]uuid.UUID
	for _, p := range placements {
		if !p.PublishedAt.Before(end) || (!p.RemovedAt.IsZero() && p.RemovedAt.Before(start)) {
			continue // публикация месяц не пересекает
		}
		var from int64 // публикация этого месяца начинает с 0
		if p.PublishedAt.Before(start) && p.HasStart {
			from = p.ViewsAtStart
		}
		endedHere := !p.RemovedAt.IsZero() && p.RemovedAt.Before(end)
		to := p.ViewsAtEnd
		if endedHere {
			to = p.FinalViews
		} else if !p.HasEnd {
			to = from
		}
		imps := max(to-from, 0)
		shownFrom := maxTime(p.PublishedAt, start)
		shownTo := end.Add(-time.Nanosecond)
		if endedHere {
			shownTo = p.RemovedAt
		}
		key := [2]uuid.UUID{p.CreativeID, p.PlatformID}
		line, ok := byPair[key]
		if !ok {
			line = &StatisticsLine{CreativeID: p.CreativeID, PlatformID: p.PlatformID, PlacementID: p.PlacementID,
				ShownFrom: dateOf(shownFrom), ShownTo: dateOf(shownTo)}
			line.Finance.Services = TaxTotal{Rate: p.Finance.Services.Rate, Known: p.Finance.Services.Known}
			byPair[key] = line
			order = append(order, key)
		}
		line.Impressions += imps
		if d := dateOf(shownFrom); d.Before(line.ShownFrom) {
			line.ShownFrom = d
		}
		if d := dateOf(shownTo); d.After(line.ShownTo) {
			line.ShownTo = d
		}
		if endedHere {
			line.Amount += p.AdvertiserAmount
			line.Finance.Services.Add(p.Finance.Services)
			line.Placements = append(line.Placements, p)
		}
	}
	out := make([]StatisticsLine, 0, len(order))
	for _, k := range order {
		out = append(out, *byPair[k])
	}
	return out
}

// InvoiceDraft — акт месяца по договору.
type InvoiceDraft struct {
	Finance    ReportFinance
	ContractID uuid.UUID
	Kind       ContractKind
	Amount     int64
	Items      []InvoiceItem
}

// InvoiceItem — строка разбивки акта по паре «креатив + площадка».
type InvoiceItem struct {
	Tax        TaxTotal
	CreativeID uuid.UUID
	PlatformID uuid.UUID
	Amount     int64
}

// BuildInvoices — акты месяца (R-20): по изначальному договору — суммы рекламодателя, по договору
// с блогером — вознаграждения; разбивка — по парам. Договор без суммы акта не получает.
// Порядок — по виду договора, затем по договору: номера новых актов стабильны при повторе.
func BuildInvoices(lines []StatisticsLine) []InvoiceDraft {
	type key struct {
		contract uuid.UUID
		kind     ContractKind
	}
	drafts := map[key]*InvoiceDraft{}
	add := func(contract uuid.UUID, kind ContractKind, creative, platform uuid.UUID, amount int64, tax TaxTotal, commission TaxTotal) {
		if contract == uuid.Nil || amount <= 0 {
			return
		}
		k := key{contract, kind}
		d, ok := drafts[k]
		if !ok {
			d = &InvoiceDraft{ContractID: contract, Kind: kind}
			drafts[k] = d
		}
		d.Amount += amount
		d.Finance.Services.Add(tax)
		if kind == ContractInitial {
			d.Finance.Commission.Add(commission)
		}
		for i := range d.Items {
			if d.Items[i].CreativeID == creative && d.Items[i].PlatformID == platform {
				d.Items[i].Amount += amount
				d.Items[i].Tax.Add(tax)
				return
			}
		}
		d.Items = append(d.Items, InvoiceItem{CreativeID: creative, PlatformID: platform, Amount: amount, Tax: tax})
	}
	for _, l := range lines {
		for _, p := range l.Placements {
			add(p.InitialContractID, ContractInitial, l.CreativeID, l.PlatformID, p.AdvertiserAmount, p.Finance.Services, p.Finance.Commission)
			add(p.PublisherContractID, ContractPublisher, l.CreativeID, l.PlatformID, p.BloggerAmount, p.PublisherTax, TaxTotal{})
		}
	}
	out := make([]InvoiceDraft, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, *d)
	}
	slices.SortFunc(out, func(a, b InvoiceDraft) int {
		if a.Kind != b.Kind {
			return strings.Compare(string(a.Kind), string(b.Kind))
		}
		return strings.Compare(a.ContractID.String(), b.ContractID.String())
	})
	return out
}

// InvoiceNumber — номер акта: `<префикс>-<ГГГГММ>-<n>` (Q-43: номер задаёт платформа).
func InvoiceNumber(prefix string, period Period, n int) string {
	if prefix == "" {
		prefix = "MX"
	}
	return prefix + "-" + strings.ReplaceAll(period.String(), "-", "") + "-" + strconv.Itoa(n)
}

func dateOf(t time.Time) time.Time {
	m := t.In(msk)
	return time.Date(m.Year(), m.Month(), m.Day(), 0, 0, 0, 0, time.UTC)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ReportRules — параметры отчётности (07 §8).
type ReportRules struct {
	// InvoiceTypeInitial, InvoiceTypePublisher — тип акта по изначальному договору и по договору
	// с блогером (ORD_INVOICE_TYPE_INITIAL, ORD_INVOICE_TYPE_PUBLISHER).
	// ПРЕДПОЛОЖЕНИЕ (Q-43): intermediary-report и invoice.
	InvoiceTypeInitial   string
	InvoiceTypePublisher string
	// NumberPrefix — префикс номера акта (ORD_INVOICE_NUMBER_PREFIX).
	NumberPrefix string
	// AutoSend — задание 1-го числа сразу отправляет сформированный отчёт (ORD_REPORT_AUTO_SEND).
	AutoSend bool
	// ErrorDay — с этого дня следующего месяца неотправленный отчёт — оповещение (ORD_REPORT_ERROR_DAY).
	ErrorDay int
}

// WithDefaults подставляет рабочие значения.
func (r ReportRules) WithDefaults() ReportRules {
	if r.InvoiceTypeInitial == "" {
		r.InvoiceTypeInitial = "intermediary-report"
	}
	if r.InvoiceTypePublisher == "" {
		r.InvoiceTypePublisher = "invoice"
	}
	if r.NumberPrefix == "" {
		r.NumberPrefix = "MX"
	}
	if r.ErrorDay <= 0 {
		r.ErrorDay = 20
	}
	return r
}

// InvoiceType — тип акта договора вида kind.
func (r ReportRules) InvoiceType(kind ContractKind) string {
	if kind == ContractPublisher {
		return r.InvoiceTypePublisher
	}
	return r.InvoiceTypeInitial
}

// OrdInvoiceTypes — допустимые типы акта для проверки конфигурации.
// ПРЕДПОЛОЖЕНИЕ (Q-43): перечисление API v8 для актов не сверено со спецификацией.
var OrdInvoiceTypes = []string{"intermediary-report", "invoice"}
