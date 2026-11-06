package domain

// Статусы ОРД (API v8, StatusType).
const (
	OrdRequestAccepted  = "Request accepted by ORD"
	OrdSuccess          = "ORD success"
	OrdError            = "ORD error"
	OrdRejected         = "ORD rejected"
	OrdErirSyncSuccess  = "ERIR sync success"
	OrdErirSyncError    = "ERIR sync error"
	OrdErirAsyncSuccess = "ERIR async success"
	OrdErirAsyncError   = "ERIR async error"
)

// Progress — смысл статуса ОРД для синхронизации (R-3).
type Progress int

const (
	// ProgressInWork — ОРД принял запрос и обрабатывает его; неизвестный статус — тоже.
	ProgressInWork Progress = iota
	// ProgressAccepted — объект принят ОРД; опрос продолжается до ЕРИР.
	ProgressAccepted
	// ProgressFinalSuccess — объект подтверждён.
	ProgressFinalSuccess
	// ProgressFinalError — финальная ошибка.
	ProgressFinalError
)

// ClassifyOrdStatus — классификация статуса ОРД (R-3). erir = false — объект без ЕРИР
// (площадка): его финальный успех — ORD success.
// Q-40: приходит ли ERIR async success для площадок, проверяется на тестовом контуре.
func ClassifyOrdStatus(status string, erir bool) Progress {
	switch status {
	case OrdSuccess:
		if !erir {
			return ProgressFinalSuccess
		}
		return ProgressAccepted
	case OrdErirSyncSuccess:
		return ProgressAccepted
	case OrdErirAsyncSuccess:
		return ProgressFinalSuccess
	case OrdError, OrdRejected, OrdErirSyncError, OrdErirAsyncError:
		return ProgressFinalError
	}
	return ProgressInWork
}

// PassesErir сообщает, что объект вида t подтверждается ЕРИР (R-3).
func PassesErir(t ObjectType) bool {
	return t != TypePlatform
}
