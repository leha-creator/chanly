package store

import (
	"fmt"
	"slices"
	"time"
)

// Transition — строка таблицы переходов 03 §4. Смена состояния размещения
// возможна только через Tx.apply (07 §9).
type Transition struct {
	Code  string
	From  []string // пусто — создание (P-01)
	To    string
	Actor string // advertiser, blogger, system
}

// Ключ с суффиксом «:admin» или «:итог» — вариант перехода с другим актором или исходом; в истории пишется Code.
var transitions = map[string]Transition{
	"P-01":          {"P-01", nil, "created", "advertiser"},
	"P-02":          {"P-02", []string{"created"}, "awaiting_blogger", "system"},
	"P-03":          {"P-03", []string{"awaiting_blogger"}, "awaiting_schedule", "blogger"},
	"P-04":          {"P-04", []string{"awaiting_blogger"}, "declined", "blogger"},
	"P-05":          {"P-05", []string{"awaiting_blogger"}, "expired", "system"},
	"P-06":          {"P-06", []string{"created", "awaiting_blogger"}, "cancelled", "advertiser"},
	"P-07":          {"P-07", []string{"scheduled"}, "scheduled", "blogger"},
	"P-08":          {"P-08", []string{"scheduled"}, "published", "system"},
	"P-10":          {"P-10", []string{"published"}, "removed", "system"},
	"P-11":          {"P-11", []string{"awaiting_blogger", "awaiting_schedule", "scheduled"}, "created", "advertiser"},
	"P-12":          {"P-12", []string{"removed"}, "accepted", "advertiser"},
	"P-13":          {"P-13", []string{"removed"}, "accepted", "system"},
	"P-14":          {"P-14", []string{"published", "removed"}, "disputed", "advertiser"},
	"P-16":          {"P-16", []string{"accepted"}, "paid", "system"},
	"P-17":          {"P-17", []string{"awaiting_schedule"}, "scheduled", "blogger"},
	"P-18":          {"P-18", []string{"awaiting_schedule"}, "expired", "system"},
	"P-06:admin":    {"P-06", []string{"created", "awaiting_blogger", "awaiting_schedule", "scheduled"}, "cancelled", "admin"},
	"P-10:admin":    {"P-10", []string{"published"}, "removed", "admin"},
	"P-14:admin":    {"P-14", []string{"published", "removed"}, "disputed", "admin"},
	"P-15:accepted": {"P-15", []string{"disputed"}, "accepted", "admin"},
	"P-15:refunded": {"P-15", []string{"disputed"}, "refunded", "admin"},
}

// terminal — конечные состояния размещения (03 §3).
var terminal = []string{"paid", "declined", "expired", "cancelled", "failed", "refunded"}

// IsTerminal сообщает, конечное ли состояние.
func IsTerminal(status string) bool { return slices.Contains(terminal, status) }

// Allowed сообщает, допустим ли переход из текущего состояния.
func Allowed(code, status string) bool {
	t, ok := transitions[code]
	return ok && slices.Contains(t.From, status)
}

// apply выполняет переход: проверяет исходное состояние, пишет историю,
// снимает удержание при отказных исходах и пересчитывает состояние заказа.
func (tx *Tx) apply(p *Placement, code, reason string) error {
	t, ok := transitions[code]
	if !ok {
		panic("unknown transition " + code)
	}
	if t.From != nil && !slices.Contains(t.From, p.Status) {
		tx.log.Warn("placement transition rejected", "op", "Placement.Transition",
			"placement_id", p.ID, "transition", code, "status", p.Status)
		return Conflict("invalid_transition", "transition is not allowed from current status",
			map[string]any{"transition": t.Code, "status": p.Status, "allowed_from": t.From})
	}
	from := p.Status
	p.Status = t.To
	p.StatusReason = reason
	p.StatusChangedAt = tx.Now
	p.History = append(p.History, HistoryEntry{
		Transition: t.Code, From: from, To: t.To, ActorType: t.Actor, ReasonCode: reason, CreatedAt: tx.Now,
	})
	tx.log.Info("placement transition applied", "op", "Placement.Transition",
		"placement_id", p.ID, "order_id", p.OrderID, "transition", t.Code, "from", from, "to", t.To, "actor_type", t.Actor)

	switch t.To {
	case "declined", "expired", "cancelled", "failed":
		tx.releaseHold(p)
	}
	tx.notifyTransition(p, t.Code)
	if o, err := tx.Order(p.OrderID); err == nil {
		tx.recalcOrder(o)
	}
	return nil
}

// recalcOrder пересчитывает производное состояние заказа (03 §1.2).
func (tx *Tx) recalcOrder(o *Order) {
	ps := tx.OrderPlacements(o.ID)
	allCancelled, allTerminal := len(ps) > 0, len(ps) > 0
	for _, p := range ps {
		if p.Status != "cancelled" {
			allCancelled = false
		}
		if !IsTerminal(p.Status) {
			allTerminal = false
		}
	}
	prev := o.Status
	switch {
	case allCancelled:
		o.Status = "cancelled"
	case allTerminal:
		o.Status = "completed"
	default:
		o.Status = map[string]string{
			"pending_moderation": "moderation", "rejected": "rejected",
			"approved": "marking", "ready": "in_progress",
		}[o.CurrentVersion().Status]
	}
	if prev != o.Status {
		tx.log.Info("order status changed", "op", "Order.Recalc", "order_id", o.ID, "from", prev, "to", o.Status)
	}
}

func (tx *Tx) notifyTransition(p *Placement, code string) {
	o, err := tx.Order(p.OrderID)
	if err != nil {
		return
	}
	title := ""
	if c, err := tx.Channel(p.ChannelID); err == nil {
		title = c.Title
	}
	// Ссылки — как у бэкенда (notification/domain.Link): рекламодателю — заказ, блогеру — размещение.
	orderID := o.ID
	adv := func(tpl, head, text string) {
		tx.Notify(o.OrgID, tpl, head, text, "placement", p.ID, &orderID, "/adv/order/"+o.ID.String())
	}
	blg := func(tpl, head, text string) {
		tx.Notify(p.BloggerOrgID, tpl, head, text, "placement", p.ID, &orderID, "/blg/placement/"+p.ID.String())
	}
	ch := "«" + title + "»"
	switch code {
	case "P-02":
		blg("placement.request", "Новый запрос на размещение в "+ch,
			fmt.Sprintf("Рекламодатель хочет разместить пост в %s, заказ № %d.\nОтветьте до %s МСК, иначе запрос истечёт.", ch, o.Number, fmtMSK(p.AcceptDeadlineAt)))
	case "P-03":
		adv("placement.accepted", "Блогер принял размещение в "+ch,
			fmt.Sprintf("Размещение в %s по заказу № %d принято в работу. Блогер выберет время публикации.", ch, o.Number))
	case "P-04":
		adv("placement.declined", "Блогер отказался от размещения в "+ch,
			fmt.Sprintf("Размещение в %s по заказу № %d отклонено блогером. Деньги вернулись на баланс.", ch, o.Number))
	case "P-05", "P-18":
		text := fmt.Sprintf("Срок ответа или планирования публикации в %s по заказу № %d прошёл. Размещение завершено, деньги возвращены рекламодателю.", ch, o.Number)
		adv("placement.expired", "Размещение в "+ch+" истекло", text)
		blg("placement.expired", "Размещение в "+ch+" истекло", text)
	case "P-07", "P-17":
		adv("placement.scheduled", "Публикация в "+ch+" запланирована",
			fmt.Sprintf("Пост по заказу № %d выйдет в %s %s МСК.", o.Number, ch, fmtMSK(p.PublishAt)))
	case "P-08":
		adv("placement.published", "Пост опубликован в "+ch, fmt.Sprintf("Пост по заказу № %d вышел в %s.", o.Number, ch))
	case "P-10":
		adv("placement.removed", "Пост снят из "+ch,
			fmt.Sprintf("Размещение по заказу № %d завершено: пост снят из %s.\nПретензию можно открыть до %s МСК, после этого размещение будет принято автоматически.", o.Number, ch, fmtMSK(p.ClaimDeadlineAt)))
	case "P-14":
		blg("placement.disputed", "Претензия по размещению в "+ch,
			fmt.Sprintf("Рекламодатель открыл претензию по заказу № %d. Платформа рассмотрит её и сообщит решение.", o.Number))
	case "P-16":
		blg("payout.succeeded", "Выплата получена", fmt.Sprintf("Деньги за размещение в %s, заказ № %d, выплачены.", ch, o.Number))
	}
}

// ---------------------------------------------------------------- таймеры

// TimerResult — переход, выполненный таймером.
type TimerResult struct {
	PlacementID string
	Transition  string
	ToStatus    string
}

// RunTimers выполняет просроченные таймеры T-01, T-13, T-05 и истекает
// неоплаченные счета. Вызывается в начале каждой транзакции.
func (tx *Tx) RunTimers() []TimerResult {
	var out []TimerResult
	due := func(t *time.Time) bool { return t != nil && !tx.Now.Before(*t) }
	for _, p := range tx.D.Placements {
		code := ""
		switch {
		case p.Status == "awaiting_blogger" && due(p.AcceptDeadlineAt):
			code = "P-05"
		case p.Status == "awaiting_schedule" && due(p.ScheduleDeadlineAt):
			code = "P-18"
		case p.Status == "removed" && due(p.ClaimDeadlineAt):
			code = "P-13"
		}
		if code == "" {
			continue
		}
		reason := map[string]string{"P-05": "accept_timeout", "P-18": "schedule_timeout", "P-13": "auto_accept"}[code]
		if code == "P-13" {
			tx.distributeFull(p)
		}
		if err := tx.apply(p, code, reason); err == nil {
			out = append(out, TimerResult{PlacementID: p.ID.String(), Transition: code, ToStatus: p.Status})
		}
	}
	for _, inv := range tx.D.Invoices {
		if inv.Status == "issued" && !tx.Now.Before(inv.ExpiresAt) {
			inv.Status = "expired"
			tx.log.Info("invoice expired", "op", "Invoice.Expire", "invoice_id", inv.ID)
		}
	}
	if len(out) > 0 {
		tx.log.Debug("timers fired", "op", "Store.RunTimers", "count", len(out))
	}
	return out
}

// ---------------------------------------------------------------- время

// MSK — часовой пояс показа и периода времени суток кампании.
var MSK = time.FixedZone("MSK", 3*60*60)

// WindowFor возвращает начало первого и конец последнего дня периода (по МСК) в UTC.
func WindowFor(dateFrom, dateTo time.Time) (time.Time, time.Time) {
	start := time.Date(dateFrom.Year(), dateFrom.Month(), dateFrom.Day(), 0, 0, 0, 0, MSK)
	end := time.Date(dateTo.Year(), dateTo.Month(), dateTo.Day(), 23, 59, 59, 0, MSK)
	return start.UTC(), end.UTC()
}

// minutes переводит ЧЧ:ММ в минуты от начала суток.
func minutes(hhmm string) int {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return h*60 + m
}

// InTimeRange проверяет, что момент по МСК попадает в период времени суток.
func InTimeRange(t time.Time, from, to string) bool {
	l := t.In(MSK)
	m := l.Hour()*60 + l.Minute()
	return m >= minutes(from) && m <= minutes(to)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func ptr[T any](v T) *T { return &v }

func fmtMSK(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.In(MSK).Format("02.01 15:04")
}
