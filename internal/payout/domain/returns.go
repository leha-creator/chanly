package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var ErrUnsupported = errors.New("rocketwork: operation unsupported by the pinned contract")

type ReturnEvidence struct {
	ID, PayoutID, DocumentID, AdminID, ApproverID uuid.UUID
	Amount                                        money.Kopecks
	Destination, Reason                           string
	ReceivedAt                                    time.Time
}
type ReturnStore interface {
	ReturnEvidence(context.Context, uuid.UUID) (ReturnEvidence, error)
	ReturnedAmount(context.Context, uuid.UUID) (money.Kopecks, error)
	InsertReturnEvidence(context.Context, ReturnEvidence) error
}
type ReturnLedger interface {
	RecordPayoutReturn(context.Context, Payout, ReturnEvidence) error
}
