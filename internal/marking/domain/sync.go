// Package domain — правила модуля маркировки (04 §6, 05 §3): объекты ОРД, состояния
// синхронизации, классификация статусов ОРД, расписание опроса, цепочки зависимостей
// и полезная нагрузка запросов. Без базы, сети и журнала.
package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ObjectType — вид объекта ОРД.
type ObjectType string

const (
	TypeOrganization ObjectType = "organization"
	TypeContract     ObjectType = "contract"
	TypePlatform     ObjectType = "platform"
	TypeCreative     ObjectType = "creative"
)

// ObjectTypes — все виды в постоянном порядке.
var ObjectTypes = []ObjectType{TypeOrganization, TypeContract, TypePlatform, TypeCreative, TypeStatistics, TypeInvoice, TypeInvoiceCreatives}

// Valid сообщает, что вид из перечисления.
func (t ObjectType) Valid() bool {
	for _, v := range ObjectTypes {
		if v == t {
			return true
		}
	}
	return false
}

// ObjectRef — объект ОРД по идентификатору нашей записи (ord_organization.id, ord_contract.id,
// ord_platform.id, creative.id).
type ObjectRef struct {
	Type ObjectType
	ID   uuid.UUID
}

// String — вид `<type>:<id>`: ключ заданий и оповещений, аргумент `maxim admin ord retry`.
func (r ObjectRef) String() string { return string(r.Type) + ":" + r.ID.String() }

// ParseObjectRef разбирает `<type>:<id>`.
func ParseObjectRef(s string) (ObjectRef, error) {
	t, id, ok := strings.Cut(s, ":")
	if !ok || !ObjectType(t).Valid() {
		names := make([]string, 0, len(ObjectTypes))
		for _, v := range ObjectTypes {
			names = append(names, string(v))
		}
		return ObjectRef{}, fmt.Errorf("object ref %q: want <type>:<id>, type one of %s", s, strings.Join(names, ", "))
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return ObjectRef{}, fmt.Errorf("object ref %q: %w", s, err)
	}
	return ObjectRef{Type: ObjectType(t), ID: uid}, nil
}

// SyncStatus — состояние синхронизации (04 §9).
type SyncStatus string

const (
	SyncPending SyncStatus = "pending"
	SyncSent    SyncStatus = "sent"
	SyncSynced  SyncStatus = "synced"
	SyncError   SyncStatus = "error"
)

// SyncStatuses — все состояния в постоянном порядке.
var SyncStatuses = []SyncStatus{SyncPending, SyncSent, SyncSynced, SyncError}

// maxSyncError — предел текста ошибки в sync_error (R-3).
const maxSyncError = 2000

// Причины ошибки, которые система ставит сама (без ответа ОРД).
const (
	ReasonStatusTimeout    = "status_timeout"
	ReasonNameTooLong      = "name_too_long"
	ReasonOfferNotAccepted = "offer_not_accepted"
	ReasonAttemptsExceeded = "attempts_exceeded"
)

// SyncState — поля синхронизации объекта (04 §6.1) и сверх них ord_status, sent_at (R-2).
// Нулевое время — NULL.
type SyncState struct {
	// SubmissionUncertain is durably set before a report POST. No automatic
	// retry/rebuild may clear it after a lost response or legacy migration.
	SubmissionUncertain bool
	Status              SyncStatus
	ExternalID          string
	RequestID           string
	OrdStatus           string
	Error               string
	Attempts            int
	SentAt              time.Time
	SyncedAt            time.Time
	PayloadHash         string
}

// NewSyncState — объект, ожидающий первой отправки.
func NewSyncState() SyncState { return SyncState{Status: SyncPending} }

// Outcome — итог применения статуса ОРД к объекту.
type Outcome int

const (
	// OutcomeWaiting — ОРД ещё обрабатывает объект: опрос продолжается.
	OutcomeWaiting Outcome = iota
	// OutcomeSynced — финальный успех.
	OutcomeSynced
	// OutcomeFailed — финальная ошибка: объект в error.
	OutcomeFailed
)

// MarkSent — запрос принят ОРД (R-2): pending → sent. Попытка засчитывается.
func (s *SyncState) MarkSent(externalID, requestID, ordStatus, hash string, now time.Time) {
	s.SubmissionUncertain = false
	s.Status = SyncSent
	s.ExternalID = externalID
	s.RequestID = requestID
	s.OrdStatus = ordStatus
	s.PayloadHash = hash
	s.Error = ""
	s.SentAt = now
	s.SyncedAt = time.Time{}
	s.Attempts++
}

// ApplyStatus применяет статус ОРД к отправленному объекту (R-3). erir — объект проходит
// ЕРИР: у площадки финальный успех — ORD success. errs — тексты error_message[].
func (s *SyncState) ApplyStatus(ordStatus string, errs []string, erir bool, now time.Time) Outcome {
	s.OrdStatus = ordStatus
	switch ClassifyOrdStatus(ordStatus, erir) {
	case ProgressFinalSuccess:
		s.Status = SyncSynced
		s.SyncedAt = now
		s.Error = ""
		return OutcomeSynced
	case ProgressFinalError:
		s.Status = SyncError
		s.Error = joinErrors(ordStatus, errs)
		return OutcomeFailed
	}
	return OutcomeWaiting
}

// Fail переводит объект в error с причиной (ответ 4xx, исчерпание повторов, status_timeout,
// ошибка сборки запроса). attempted — причина возникла после вызова ОРД: попытка засчитывается.
func (s *SyncState) Fail(reason string, attempted bool) {
	s.Status = SyncError
	s.Error = truncate(reason, maxSyncError)
	if attempted {
		s.Attempts++
	}
}

// Retry — «Повторить» администратора (S-20): error → pending. Из других состояний — отказ.
func (s *SyncState) Retry() error {
	if s.SubmissionUncertain {
		return fmt.Errorf("marking: submission requires reconciliation")
	}
	if s.Status != SyncError {
		return &StateError{Status: s.Status}
	}
	s.Status = SyncPending
	s.Error = ""
	return nil
}

// NeedsResend сообщает, что полезная нагрузка изменилась после последней отправки.
func (s SyncState) NeedsResend(hash string) bool { return s.PayloadHash != hash }

// Requeue ставит объект на повторную отправку при новой полезной нагрузке (R-2): из synced,
// error и sent → pending. Объект в pending и с тем же хешем не меняется. true — состояние изменилось.
func (s *SyncState) Requeue(hash string) bool {
	if !s.NeedsResend(hash) {
		return false
	}
	return s.Invalidate()
}

// Invalidate ставит объект на повторную отправку без сравнения нагрузки: изменились данные,
// из которых она собирается (признак isRr контрагента). true — состояние изменилось.
func (s *SyncState) Invalidate() bool {
	if s.SubmissionUncertain {
		return false
	}
	if s.Status == SyncPending {
		return false
	}
	s.Status = SyncPending
	s.Error = ""
	return true
}

// UpToDate — объект не требует отправки: уже отправлен или подтверждён с той же нагрузкой.
func (s SyncState) UpToDate(hash string) bool {
	switch s.Status {
	case SyncSent:
		return true
	case SyncSynced:
		return !s.NeedsResend(hash)
	}
	return false
}

// Ready — зависимость готова для зависимых объектов (R-2): подтверждена или принята ОРД.
// Q-40: принимает ли ОРД договор на контрагента, ещё не подтверждённого ЕРИР, проверяется
// на тестовом контуре.
func (s SyncState) Ready() bool {
	if s.Status == SyncSynced {
		return true
	}
	return s.Status == SyncSent && ClassifyOrdStatus(s.OrdStatus, true) == ProgressAccepted
}

// StateError — действие недопустимо в текущем состоянии синхронизации.
type StateError struct{ Status SyncStatus }

func (e *StateError) Error() string {
	return fmt.Sprintf("marking: action is not allowed in sync status %s", e.Status)
}

func joinErrors(ordStatus string, errs []string) string {
	if len(errs) == 0 {
		return truncate(ordStatus, maxSyncError)
	}
	return truncate(strings.Join(errs, "; "), maxSyncError)
}

// truncate обрезает строку до n рун.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
