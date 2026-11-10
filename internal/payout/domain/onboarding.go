package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RegistrationNames must be entered explicitly. The full legal name is never
// guessed/split to overwrite an existing provider profile.
type RegistrationNames struct {
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	SecondName string `json:"second_name,omitempty"`
}
type OnboardingInput struct {
	INN                    string            `json:"inn"`
	Phone                  string            `json:"phone"`
	Names                  RegistrationNames `json:"names"`
	ExecutorID             int64             `json:"executor_id"`
	WithFrameworkAgreement bool              `json:"with_framework_agreement"`
}

func (i OnboardingInput) Snapshot(action string) ([]byte, string, error) {
	if i.INN == "" || i.Phone == "" {
		return nil, "", ErrInvalidPayout
	}
	switch action {
	case "invite":
	case "register", "update":
		if i.Names.FirstName == "" || i.Names.LastName == "" {
			return nil, "", fmt.Errorf("%w: explicit names required", ErrInvalidPayout)
		}
	case "rebind":
		if i.ExecutorID <= 0 {
			return nil, "", ErrInvalidPayout
		}
	default:
		return nil, "", ErrInvalidPayout
	}
	if action == "update" && i.ExecutorID <= 0 {
		return nil, "", ErrInvalidPayout
	}
	body, err := json.Marshal(i)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(body)
	return body, hex.EncodeToString(hash[:]), nil
}

type Onboarding struct {
	Scope                ProviderScope
	OrganizationID       uuid.UUID
	Action               string
	Input                OnboardingInput
	PayloadHash          string
	CorrelationID        uuid.UUID
	State                OperationState
	Result               string
	CreatedAt, UpdatedAt time.Time
}
type OnboardingStore interface {
	PrepareOnboarding(context.Context, Onboarding) (Onboarding, error)
	Onboarding(context.Context, ProviderScope, uuid.UUID, string) (Onboarding, error)
	TransitionOnboarding(context.Context, Onboarding, OperationState) (bool, error)
}
type OnboardingProvider interface {
	ExecuteOnboarding(context.Context, string, OnboardingInput) (string, error)
}
