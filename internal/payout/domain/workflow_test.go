package domain

import (
	"strings"
	"testing"
)

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
