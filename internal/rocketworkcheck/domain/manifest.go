package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Terms preserve the explicit contract representation, without provider SDK types.
type Terms struct {
	ExternalID, ExecutorINN, EmploymentKind                                         string
	AmountKopecks                                                                   int64
	Title, Description                                                              string
	SignatureForm, DocumentName, DocumentNumber, DocumentDate, PeriodFrom, PeriodTo string
	ActRequired, TaxRetention, Autosignable, Autocomplete, Autopayable              bool
}

type Manifest struct {
	LegacyImportID    string   `json:"legacy_import_id,omitempty"`
	ImportedTermsHash string   `json:"imported_terms_hash,omitempty"`
	Version           int      `json:"version"`
	RunID             string   `json:"run_id"`
	GrantID           string   `json:"grant_id"`
	Purpose           string   `json:"purpose"`
	Target            string   `json:"target"`
	Origin            string   `json:"origin"`
	AccountID         string   `json:"account_id"`
	AccountKey        string   `json:"account_key"`
	CompanyINN        string   `json:"company_inn"`
	Executor          Executor `json:"executor"`
	Terms             Terms    `json:"terms"`
	Actions           []Action `json:"actions"`
}

func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Observation struct {
	ProviderPaidAt                          *time.Time
	CanceledNoCharge                        bool
	TaskID                                  *int64
	Status                                  string
	Ready                                   bool
	Nominal                                 int64
	Fee                                     *int64
	CanComplete, CanSign, CanPay, CanCancel bool
}
