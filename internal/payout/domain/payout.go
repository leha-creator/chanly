package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Status — состояние попытки выплаты (04 §7, payout.status).
type Status string

const (
	StatusCreated   Status = "created"
	StatusSent      Status = "sent"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Final — исход известен, попытка больше не меняется.
func (s Status) Final() bool { return s == StatusSucceeded || s == StatusFailed }

// Method — способ выплаты (R-3).
type Method string

const (
	// MethodRocketwork — через депозит Рокет Ворк (самозанятый, ИП).
	MethodRocketwork Method = "rocketwork"
	// MethodBankTransfer — юрлицу переводом вне сервиса, отмечает администратор (A-36).
	MethodBankTransfer Method = "bank_transfer"
)

// MethodFor — способ выплаты по типу лица блогера.
func MethodFor(t LegalType) Method {
	if ContractorRequired(t) {
		return MethodRocketwork
	}
	return MethodBankTransfer
}

// FailureCode — наш код отказа (05 §4.3); адаптер приводит к нему ответы сервиса.
type FailureCode string

const (
	FailureContractorNotActive FailureCode = "contractor_not_active"
	FailureTaxStatusLost       FailureCode = "tax_status_lost"
	FailureIncomeLimitExceeded FailureCode = "income_limit_exceeded"
	FailureInsufficientDeposit FailureCode = "insufficient_deposit"
	FailureProviderError       FailureCode = "provider_error"
)

// Valid — код из перечня.
func (c FailureCode) Valid() bool {
	switch c {
	case FailureContractorNotActive, FailureTaxStatusLost, FailureIncomeLimitExceeded,
		FailureInsufficientDeposit, FailureProviderError:
		return true
	}
	return false
}

// AutoRetried — отказ повторяется автоматически (R-7, R-8): нехватка депозита — каждый час
// без предела, иная ошибка — по правилам задания. Остальные — окончательный failed, повтор
// только командой администратора (retry_payout).
func (c FailureCode) AutoRetried() bool {
	return c == FailureInsufficientDeposit || c == FailureProviderError
}

// NeedsAdmin — отказ требует оповещения администратора (05 §4.3).
func (c FailureCode) NeedsAdmin() bool {
	return c == FailureTaxStatusLost || c == FailureIncomeLimitExceeded || c == FailureProviderError ||
		c == FailureInsufficientDeposit
}

// Payout — одна попытка выплаты по размещению.
type Payout struct {
	ID                    uuid.UUID
	PlacementID           uuid.UUID
	BloggerOrganizationID uuid.UUID
	Method                Method
	// ContractorID — пусто при bank_transfer.
	ContractorID   uuid.UUID
	Amount         money.Kopecks
	Attempt        int
	Status         Status
	ExternalID     string
	ProviderFee    money.Kopecks
	ReceiptURL     string
	BankReference  string
	FailureCode    FailureCode
	FailureMessage string
	SentAt         *time.Time
	CompletedAt    *time.Time
	IdempotencyKey string
	LockVersion    int
	// CreatedBy — администратор, создавший попытку повтором; пусто — система.
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IdempotencyKey — ключ попытки для Рокет Ворк (R-6).
func IdempotencyKey(payoutID uuid.UUID) string { return "rw-payout:" + payoutID.String() }

// NewPayout — первая попытка в created. Для rocketwork нужен исполнитель.
func NewPayout(id, placementID, bloggerOrgID uuid.UUID, method Method, contractorID uuid.UUID,
	amount money.Kopecks, now time.Time) (Payout, error) {
	if amount <= 0 {
		return Payout{}, fmt.Errorf("%w: amount must be positive", ErrInvalidPayout)
	}
	if (method == MethodRocketwork) == (contractorID == uuid.Nil) {
		return Payout{}, fmt.Errorf("%w: method %s and contractor do not match", ErrInvalidPayout, method)
	}
	return Payout{
		ID: id, PlacementID: placementID, BloggerOrganizationID: bloggerOrgID, Method: method,
		ContractorID: contractorID, Amount: amount, Attempt: 1, Status: StatusCreated,
		IdempotencyKey: IdempotencyKey(id), CreatedAt: now, UpdatedAt: now,
	}, nil
}

// NextAttempt — новая попытка после failed (R-9): новая строка, новый ключ.
func (p Payout) NextAttempt(id, contractorID, adminID uuid.UUID, now time.Time) (Payout, error) {
	if p.Status != StatusFailed {
		return Payout{}, fmt.Errorf("%w: attempt %d is %s", ErrPayoutNotRetryable, p.Attempt, p.Status)
	}
	if p.Method != MethodRocketwork {
		return Payout{}, fmt.Errorf("%w: %s is paid by an admin", ErrPayoutNotRetryable, p.Method)
	}
	next, err := NewPayout(id, p.PlacementID, p.BloggerOrganizationID, p.Method, contractorID, p.Amount, now)
	if err != nil {
		return Payout{}, err
	}
	next.Attempt = p.Attempt + 1
	next.CreatedBy = adminID
	return next, nil
}

// MarkSent — Рокет Ворк принял выплату. Повтор с тем же идентификатором — no-op.
func (p *Payout) MarkSent(externalID string, at time.Time) (changed bool, err error) {
	if externalID == "" {
		return false, fmt.Errorf("%w: empty external id", ErrInvalidPayout)
	}
	switch p.Status {
	case StatusCreated:
		p.Status, p.ExternalID = StatusSent, externalID
		sent := at
		p.SentAt, p.UpdatedAt = &sent, at
		return true, nil
	case StatusSent:
		if p.ExternalID == externalID {
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: sent from %s", ErrInvalidPayoutState, p.Status)
}

// MarkSucceeded — деньги дошли (R-5). Повтор с теми же данными — no-op, с другими — ошибка.
func (p *Payout) MarkSucceeded(fee money.Kopecks, receiptURL, bankRef string, at time.Time) (changed bool, err error) {
	if fee < 0 {
		return false, fmt.Errorf("%w: negative provider fee", ErrInvalidPayout)
	}
	switch p.Status {
	case StatusCreated, StatusSent:
		if p.Status == StatusCreated && p.Method == MethodRocketwork {
			// Сразу «выполнено» в ответе на создание: отправлено и выполнено одновременно.
			sent := at
			p.SentAt = &sent
		}
		p.Status, p.ProviderFee, p.ReceiptURL, p.BankReference = StatusSucceeded, fee, receiptURL, bankRef
		done := at
		p.CompletedAt, p.UpdatedAt = &done, at
		return true, nil
	case StatusSucceeded:
		if p.ProviderFee == fee && p.ReceiptURL == receiptURL && p.BankReference == bankRef {
			return false, nil
		}
		return false, fmt.Errorf("%w: succeeded again with other data", ErrInvalidPayoutState)
	}
	return false, fmt.Errorf("%w: succeeded from %s", ErrInvalidPayoutState, p.Status)
}

// MarkFailed — окончательный отказ попытки (R-8). Повтор с тем же кодом — no-op.
func (p *Payout) MarkFailed(code FailureCode, message string, at time.Time) (changed bool, err error) {
	if !code.Valid() {
		return false, fmt.Errorf("%w: unknown failure code %q", ErrInvalidPayout, code)
	}
	switch p.Status {
	case StatusCreated, StatusSent:
		p.Status, p.FailureCode, p.FailureMessage = StatusFailed, code, truncate(message, 1000)
		done := at
		p.CompletedAt, p.UpdatedAt = &done, at
		return true, nil
	case StatusFailed:
		if p.FailureCode == code {
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: failed from %s", ErrInvalidPayoutState, p.Status)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ProviderPayout — выплата в ответе адаптера в наших терминах (R-2): sent, succeeded, failed.
type ProviderPayout struct {
	ExternalID     string
	Status         Status
	Fee            money.Kopecks
	ReceiptURL     string
	FailureCode    FailureCode
	FailureMessage string
}
