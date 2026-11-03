package domain

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Отчёт по заказу (S-09, R-14 и R-15 этапа «Уведомления и отчёт по кампании»): строки по
// размещениям, итог, ссылки поста с UTM-метками и выгрузка CSV.

// ReportRow — строка отчёта по размещению. Spent + Refunded + Held — цена размещения или 0
// (размещение отменено до удержания).
type ReportRow struct {
	PlacementID uuid.UUID
	Channel     ChannelRef
	Status      Status
	Spent       money.Kopecks
	Refunded    money.Kopecks
	Held        money.Kopecks
	PublishedAt *time.Time
	RemovedAt   *time.Time
	// Views — последнее считанное число просмотров (Q-08); nil — не считывалось.
	Views          *int
	PublicationURL string
	// CPV — стоимость одного просмотра: Spent / Views с округлением вниз (D-28); nil —
	// просмотров нет или ноль.
	CPV *money.Kopecks
}

// ReportTotals — итог отчёта: суммы строк. CPV — потрачено, делённое на суммарные просмотры (D-28).
type ReportTotals struct {
	Placements int
	Published  int
	Views      int
	Spent      money.Kopecks
	Refunded   money.Kopecks
	Held       money.Kopecks
	CPV        *money.Kopecks
}

// UTMMarks — метки ссылок поста заказа (A-21).
type UTMMarks struct {
	Source   string
	Medium   string
	Campaign string
}

// Report — отчёт по заказу.
type Report struct {
	Order        Order
	CampaignName string
	// Links — адреса ссылок текущей версии поста с UTM-метками, как ушли в канал.
	Links  []string
	UTM    UTMMarks
	Rows   []ReportRow
	Totals ReportTotals
}

// ReportInput — данные размещения для строки отчёта.
type ReportInput struct {
	Placement   Placement
	Channel     ChannelRef
	Publication *Publication
	// HoldActive — у размещения есть активное удержание.
	HoldActive bool
}

// UTMCampaign — значение utm_campaign заказа (A-21).
func UTMCampaign(orderNumber int64) string { return fmt.Sprintf("order_%d", orderNumber) }

// CPV — стоимость просмотра в копейках с округлением вниз; без просмотров — nil (D-28).
func CPV(spent money.Kopecks, views *int) *money.Kopecks {
	if views == nil || *views <= 0 || spent <= 0 {
		return nil
	}
	v := spent / money.Kopecks(*views)
	return &v
}

// NewReportRow — строка отчёта (R-14): принятое и выплаченное — цена за вычетом возврата;
// возвращённое и конечное без удержания — возврат всей цены; иначе — удержано.
func NewReportRow(in ReportInput) ReportRow {
	p := in.Placement
	row := ReportRow{PlacementID: p.ID, Channel: in.Channel, Status: p.Status, PublishedAt: p.PublishedAt, RemovedAt: p.RemovedAt,
		Views: p.FinalViews}
	if pub := in.Publication; pub != nil {
		row.PublicationURL = pub.URL
		if row.PublishedAt == nil {
			at := pub.PublishedAt
			row.PublishedAt = &at
		}
		if row.RemovedAt == nil {
			row.RemovedAt = pub.RemovedAt
		}
		// ПРЕДПОЛОЖЕНИЕ: просмотры MAX не убывают (E-4): последнее считанное значение публикации
		// не меньше итогового у размещения, берётся большее.
		if pub.Views != nil && (row.Views == nil || *pub.Views > *row.Views) {
			v := *pub.Views
			row.Views = &v
		}
	}
	switch {
	case p.Status == StatusAccepted || p.Status == StatusPaid:
		var refund money.Kopecks
		if p.RefundAmount != nil {
			refund = *p.RefundAmount
		}
		row.Spent, row.Refunded = p.TotalAmount-refund, refund
	case in.HoldActive:
		row.Held = p.TotalAmount
	case p.Status.Terminal():
		row.Refunded = p.TotalAmount
	}
	row.CPV = CPV(row.Spent, row.Views)
	return row
}

// BuildReport собирает отчёт: строки в порядке ввода, итог — суммы строк.
func BuildReport(o Order, campaignName string, links []string, source, medium string, in []ReportInput) Report {
	if links == nil {
		links = []string{}
	}
	r := Report{Order: o, CampaignName: campaignName, Links: links, Rows: make([]ReportRow, 0, len(in)),
		UTM: UTMMarks{Source: source, Medium: medium, Campaign: UTMCampaign(o.Number)}}
	for _, x := range in {
		row := NewReportRow(x)
		r.Rows = append(r.Rows, row)
		t := &r.Totals
		t.Placements++
		if row.PublishedAt != nil {
			t.Published++
		}
		if row.Views != nil {
			t.Views += *row.Views
		}
		t.Spent += row.Spent
		t.Refunded += row.Refunded
		t.Held += row.Held
	}
	views := r.Totals.Views
	r.Totals.CPV = CPV(r.Totals.Spent, &views)
	return r
}

// statusLabels — состояния размещения для выгрузки (как в кабинете).
var statusLabels = map[Status]string{
	StatusCreated: "Ожидает отправки", StatusAwaitingBlogger: "Ждёт ответа блогера",
	StatusAwaitingSchedule: "Блогер выбирает время", StatusScheduled: "Запланировано", StatusPublished: "Опубликовано",
	StatusRemoved: "Снято, ждёт приёмки", StatusDisputed: "Спор", StatusAccepted: "Принято", StatusPaid: "Выплачено",
	StatusDeclined: "Отказ блогера", StatusExpired: "Истекло", StatusCancelled: "Отменено", StatusFailed: "Не опубликовано",
	StatusRefunded: "Возвращено",
}

// utf8BOM — метка порядка байт: Excel без неё читает CSV не в UTF-8.
const utf8BOM = "\uFEFF"

// reportHeader — колонки выгрузки CSV (R-15).
var reportHeader = []string{"Канал", "Ссылка на канал", "Состояние", "Опубликовано (МСК)", "Снято (МСК)", "Просмотры",
	"CPV, ₽", "Списано, ₽", "Возвращено, ₽", "Удержано, ₽", "Ссылка на пост", "utm_campaign"}

// WriteReportCSV пишет отчёт в CSV для Excel (R-15): UTF-8 с BOM, разделитель «;», суммы в рублях
// с запятой, время по Москве; последняя строка — итог.
func WriteReportCSV(w io.Writer, r Report) error {
	if _, err := io.WriteString(w, utf8BOM); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	if err := cw.Write(reportHeader); err != nil {
		return err
	}
	campaign := r.UTM.Campaign
	for _, row := range r.Rows {
		label := statusLabels[row.Status]
		if label == "" {
			label = string(row.Status)
		}
		if err := cw.Write([]string{safeCell(row.Channel.Title), safeCell(row.Channel.Link), label, mskCell(row.PublishedAt), mskCell(row.RemovedAt),
			intCell(row.Views), rubCell(row.CPV), rub(row.Spent), rub(row.Refunded), rub(row.Held), safeCell(row.PublicationURL), campaign}); err != nil {
			return err
		}
	}
	t := r.Totals
	views := t.Views
	if err := cw.Write([]string{"Итого", "", fmt.Sprintf("Размещений: %d, опубликовано: %d", t.Placements, t.Published), "", "",
		intCell(&views), rubCell(t.CPV), rub(t.Spent), rub(t.Refunded), rub(t.Held), "", campaign}); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// safeCell защищает текст от исполнения формулой в Excel (CSV injection): название канала задаёт
// блогер, поэтому значение, начинающееся с =, +, -, @, табуляции или перевода строки, получает
// ведущий апостроф.
func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r\n", rune(s[0])) {
		return "'" + s
	}
	return s
}

// rub — копейки рублями с запятой: 1150000 → «11500,00».
func rub(k money.Kopecks) string {
	sign := ""
	if k < 0 {
		sign, k = "-", -k
	}
	return fmt.Sprintf("%s%d,%02d", sign, int64(k)/100, int64(k)%100)
}

func rubCell(k *money.Kopecks) string {
	if k == nil {
		return ""
	}
	return rub(*k)
}

func intCell(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func mskCell(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(msk).Format("02.01.2006 15:04")
}
