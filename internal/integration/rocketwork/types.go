// Package rocketwork — адаптер Рокет Ворк (05 §4): исполнители, депозит, выплаты, вебхуки.
// Контракт адаптера — 05 §4.2 в наших терминах; приведение статусов и кодов сервиса к нашим
// делается здесь (R-2), модуль выплат форматов сервиса не знает.
//
// Live-чтение и callbacks сделок сверены с roketwork_openapi.json (Public API v1). Запись
// заблокированы до настройки договоров и идентификации исполнителей (Q-10–12).
// Legacy status helpers ниже используются только локальной подделкой и её тестами.
//
// Реализации: Client (ROCKETWORK_MODE=live), Disabled (disabled), rocketworkfake (fake).
package rocketwork

import (
	"errors"
	"fmt"
)

// Наши статусы исполнителя (04 §7).
const (
	ContractorInvited = "invited"
	ContractorPending = "pending"
	ContractorActive  = "active"
	ContractorBlocked = "blocked"
)

// Наши статусы выплаты (04 §7): created не бывает в ответе сервиса.
const (
	PaymentSent      = "sent"
	PaymentSucceeded = "succeeded"
	PaymentFailed    = "failed"
)

// Наши коды отказов (05 §4.3).
const (
	FailureContractorNotActive = "contractor_not_active"
	FailureTaxStatusLost       = "tax_status_lost"
	FailureIncomeLimitExceeded = "income_limit_exceeded"
	FailureInsufficientDeposit = "insufficient_deposit"
	FailureProviderError       = "provider_error"
)

// Виды формы исполнителя в запросе.
const (
	LegalFormSelfEmployed = "self_employed"
	LegalFormIndividual   = "individual_entrepreneur"
)

// ContractorRequest — создание исполнителя (EnsureContractor).
type ContractorRequest struct {
	// ExternalID — наш идентификатор организации.
	ExternalID string
	INN        string
	FullName   string
	Phone      string
	LegalForm  string
}

// Contractor — исполнитель в наших терминах.
type Contractor struct {
	ID            string
	Status        string
	BlockedReason string
	OnboardingURL string
	// RawStatus — статус сервиса как есть (для журнала DEBUG); Known — статус знаком адаптеру.
	RawStatus string
	Known     bool
}

// PaymentRequest — команда на выплату (CreatePayout).
type PaymentRequest struct {
	IdempotencyKey string
	// ExternalID — наш идентификатор попытки выплаты.
	ExternalID    string
	ContractorID  string
	AmountKopecks int64
	Purpose       string
}

// Payment — выплата в наших терминах.
type Payment struct {
	AmountKopecks         int64
	ExecutorAmountKopecks *int64
	ID                    string
	ExternalID            string
	Status                string
	FeeKopecks            int64
	ReceiptURL            string
	FailureCode           string
	FailureMessage        string
	RawStatus             string
	Known                 bool
}

// Виды событий вебхука в наших терминах.
const (
	EventContractorStatus = "contractor_status"
	EventPaymentStatus    = "payout_status"
)

// Event — проверенное событие вебхука: только идентификатор объекта (R-10).
type Event struct {
	Kind     string
	ObjectID string
	EventID  string
}

var (
	// ErrDisabled — режим disabled: вызовы не выполняются (R-1).
	ErrDisabled = errors.New("rocketwork: disabled")
	// ErrUnavailable — временная ошибка сервиса.
	ErrUnavailable = errors.New("rocketwork: unavailable")
	// ErrNotFound — объекта нет в сервисе.
	ErrNotFound = errors.New("rocketwork: not found")
	// ErrSignature — подпись вебхука не прошла проверку.
	ErrSignature = errors.New("rocketwork: bad webhook signature")
	// ErrFormat — тело вебхука или ответа не разобрано.
	ErrFormat = errors.New("rocketwork: bad format")
)

// RejectedError — окончательный отказ сервиса с нашим кодом.
type RejectedError struct {
	Code    string
	Message string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("rocketwork rejected: %s: %s", e.Code, e.Message)
}

// ContractorStatusFrom приводит статус исполнителя сервиса к нашему (R-2).
// Неизвестный — pending и known=false: объект продолжают опрашивать.
//
// Локальная модель fake; это не payment_readiness из Public API v1.
func ContractorStatusFrom(raw string) (status, blockedReason string, known bool) {
	switch raw {
	case "invited":
		return ContractorInvited, "", true
	case "registering", "documents_check":
		return ContractorPending, "", true
	case "active":
		return ContractorActive, "", true
	case "fns_unbound":
		return ContractorBlocked, FailureTaxStatusLost, true
	case "blocked", "deleted":
		return ContractorBlocked, raw, true
	}
	return ContractorPending, "", false
}

// PaymentStatusFrom приводит статус выплаты сервиса к нашему (R-2). Неизвестный — sent.
//
// Локальная модель fake; live использует taskDTO.payment.
func PaymentStatusFrom(raw string) (status string, known bool) {
	switch raw {
	case "new", "processing":
		return PaymentSent, true
	case "paid":
		return PaymentSucceeded, true
	case "failed", "rejected":
		return PaymentFailed, true
	}
	return PaymentSent, false
}

// FailureCodeFrom приводит причину отказа сервиса к нашему коду (05 §4.3).
//
// Локальные сценарии отказов fake; live не выводит бизнес-код из свободного текста.
func FailureCodeFrom(raw string) string {
	switch raw {
	case "contractor_not_active", "contractor_blocked":
		return FailureContractorNotActive
	case "tax_status_lost", "fns_unbound":
		return FailureTaxStatusLost
	case "income_limit_exceeded":
		return FailureIncomeLimitExceeded
	case "insufficient_funds":
		return FailureInsufficientDeposit
	}
	return FailureProviderError
}

// EventKindFrom приводит вид события сервиса к нашему.
//
// Локальные события fake; это не стримы Public API v1.
func EventKindFrom(raw string) (string, bool) {
	switch raw {
	case "contractor.status_changed":
		return EventContractorStatus, true
	case "payment.status_changed":
		return EventPaymentStatus, true
	}
	return "", false
}
