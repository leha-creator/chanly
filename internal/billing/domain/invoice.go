// Package domain — правила приёма оплаты (05 §5, 06 §2.5): счета на оплату, сопоставление
// поступлений из выписки, период и номер закрывающих документов. Без базы, сети и журнала.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var msk = time.FixedZone("MSK", 3*60*60)

// InvoiceStatus — состояние счёта на оплату (04 §7, invoice.status).
type InvoiceStatus string

const (
	InvoiceIssued    InvoiceStatus = "issued"
	InvoicePaid      InvoiceStatus = "paid"
	InvoiceExpired   InvoiceStatus = "expired"
	InvoiceCancelled InvoiceStatus = "cancelled"
)

// Пределы суммы счёта — как в InvoiceCreate спецификации: 1 000 ₽ … 1 000 000 000 ₽.
const (
	MinInvoiceAmount           money.Kopecks = 100000
	MinControlledInvoiceAmount money.Kopecks = 100
	MaxInvoiceAmount           money.Kopecks = 100000000000
)

// Payer — плательщик на момент выставления (D-26, R-10). Банковские реквизиты необязательны.
type Payer struct {
	Name            string
	INN             string
	BankAccount     string
	BankName        string
	BankBIK         string
	BankCorrAccount string
}

// HasBankDetails — в счёте печатаются банковские реквизиты плательщика.
func (p Payer) HasBankDetails() bool { return p.BankAccount != "" && p.BankBIK != "" }

// Invoice — счёт на пополнение баланса (S-06).
type Invoice struct {
	ID             uuid.UUID
	Number         string
	OrganizationID uuid.UUID
	Amount         money.Kopecks
	Status         InvoiceStatus
	ExpiresAt      time.Time
	PaidAt         *time.Time
	PaidAmount     *money.Kopecks
	Payer          Payer
	PDFStorageKey  string
	CreatedBy      uuid.UUID
	CancelReason   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ValidateAmount — сумма счёта в пределах.
func ValidateAmount(amount money.Kopecks) error {
	if amount < MinInvoiceAmount || amount > MaxInvoiceAmount {
		return ErrAmountOutOfRange
	}
	return nil
}

// FormatNumber — номер счёта MX- и не меньше шести цифр (R-1).
func FormatNumber(seq int64) string { return fmt.Sprintf("MX-%06d", seq) }

// numberPattern — номер счёта в назначении платежа (05 §5 шаг 2). Плательщики пишут его
// по-разному: латиницей или кириллицей («МХ»), с пробелом или без дефиса.
var numberPattern = regexp.MustCompile(`(?i)(?:MX|МХ|MХ|МX)\s*[-–—]?\s*(\d{6,})`)

// FindNumbers — номера счетов в назначении платежа в каноническом виде, без повторов.
func FindNumbers(purpose string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range numberPattern.FindAllStringSubmatch(purpose, -1) {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		num := FormatNumber(n)
		if !seen[num] {
			seen[num] = true
			out = append(out, num)
		}
	}
	return out
}

// ExpiresAt — срок действия счёта (14 суток, 04 §7).
func ExpiresAt(now time.Time, ttl time.Duration) time.Time { return now.Add(ttl) }

// Payable — по счёту можно зачислить поступление: выставлен или истёк (S-06).
func (i *Invoice) Payable() bool { return i.Status == InvoiceIssued || i.Status == InvoiceExpired }

// MarkPaid — поступление сопоставлено: фактическая сумма может отличаться от счёта (S-30).
func (i *Invoice) MarkPaid(amount money.Kopecks, now time.Time) error {
	if !i.Payable() {
		return fmt.Errorf("%w: pay invoice in status %s", ErrInvalidTransition, i.Status)
	}
	if amount <= 0 {
		return fmt.Errorf("%w: paid amount must be positive", ErrInvalidTransition)
	}
	i.Status = InvoicePaid
	i.PaidAt = &now
	i.PaidAmount = &amount
	i.UpdatedAt = now
	return nil
}

// Expire — срок вышел (R-11): issued → expired.
func (i *Invoice) Expire(now time.Time) error {
	if i.Status != InvoiceIssued || now.Before(i.ExpiresAt) {
		return fmt.Errorf("%w: expire invoice in status %s", ErrInvalidTransition, i.Status)
	}
	i.Status = InvoiceExpired
	i.UpdatedAt = now
	return nil
}

// Cancel — отмена администратором с основанием (R-11).
func (i *Invoice) Cancel(reason string, now time.Time) error {
	if !i.Payable() {
		return fmt.Errorf("%w: cancel invoice in status %s", ErrInvalidTransition, i.Status)
	}
	if reason == "" {
		return fmt.Errorf("%w: cancel reason is required", ErrInvalidTransition)
	}
	i.Status = InvoiceCancelled
	i.CancelReason = reason
	i.UpdatedAt = now
	return nil
}

// Discrepancy — разница поступления и суммы счёта (S-30 «с расхождением»); ноль — совпало.
func (i *Invoice) Discrepancy() money.Kopecks {
	if i.PaidAmount == nil {
		return 0
	}
	return *i.PaidAmount - i.Amount
}
