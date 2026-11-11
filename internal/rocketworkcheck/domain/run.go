package domain

import (
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
)

type Run struct {
	ProviderPaidAt                                   *time.Time
	ID, AccountID, GrantID, ExternalID, ManifestHash string
	Manifest                                         []byte
	TaskID                                           *int64
	Nominal, FeeCeiling                              money.Kopecks
	State                                            string
	ReservedAt, SettledAt                            *time.Time
	ActualNominal, ActualFee                         *money.Kopecks
	LedgerID                                         *string
}

type Grant struct {
	ID       string
	Policy   Policy
	IssuedBy string
}

type Checkpoint struct {
	RunID              string
	Action             Action
	State, RequestHash string
}
