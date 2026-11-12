package domain

import (
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestAgreementCommissionPolicy(t *testing.T) {
	a := AgreementTerms{MaxCommission: 100}
	for _, fee := range []money.Kopecks{-1, 0, 100, 101} {
		if a.AllowsCommission(fee) != (fee >= 0 && fee <= 100) {
			t.Fatalf("capped commission %d", fee)
		}
	}
	a.MaxCommission, a.CommissionUnlimited = 0, true
	if a.AllowsCommission(-1) || !a.AllowsCommission(1000000) {
		t.Fatal("unlimited policy must accept actual nonnegative fees")
	}
	base := AgreementTerms{Amount: 1000, Currency: "RUB", ExecutorID: 7, EmploymentKind: "selfemployed", Title: "synthetic", Description: "synthetic service", SignatureForm: "manual", DocumentNumber: "synthetic", DocumentDate: "2026-11-12"}
	_, before, err := base.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	base.CommissionUnlimited = true
	_, after, err := base.Snapshot()
	if err != nil || before == after {
		t.Fatal("commission policy must be part of the immutable snapshot")
	}
}

func TestUncertainOperationCannotBeReplayed(t *testing.T) {
	for _, from := range []OperationState{OperationInFlight, OperationUncertain, OperationConfirmed, OperationRejected} {
		if CanTransitionOperation(from, OperationPrepared) || CanTransitionOperation(from, OperationInFlight) {
			t.Errorf("replay from %s", from)
		}
	}
	if !CanTransitionOperation(OperationPrepared, OperationInFlight) || !CanTransitionOperation(OperationUncertain, OperationConfirmed) {
		t.Fatal("valid transition rejected")
	}
}

func TestAgreementSnapshotIsExactAndStable(t *testing.T) {
	a := AgreementTerms{Amount: 9223372036854775807, Currency: "RUB", ExecutorID: 7, EmploymentKind: "entrepreneur", Title: "synthetic", Description: "synthetic service", SignatureForm: "manual", DocumentNumber: "synthetic-1", DocumentDate: "2026-11-09"}
	body, hash, err := a.Snapshot()
	if err != nil || !strings.Contains(string(body), "9223372036854775807") {
		t.Fatalf("snapshot: %v", err)
	}
	_, same, _ := a.Snapshot()
	if same != hash {
		t.Fatal("unstable hash")
	}
	a.ActRequired = true
	_, changed, _ := a.Snapshot()
	if changed == hash {
		t.Fatal("terms not hashed")
	}
	a.DocumentDate = ""
	if _, _, err := a.Snapshot(); err == nil {
		t.Fatal("missing agreement accepted")
	}
}
