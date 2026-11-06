package domain

import (
	"math"
	"testing"
)

func TestReportTaxSnapshots(t *testing.T) {
	for _, tc := range []struct {
		gross int64
		rate  string
		vat   int64
		wire  string
	}{
		{12200, "vat_22", 2200, "22"}, {10700, "vat_7", 700, "7"}, {10500, "vat_5", 500, "5"},
		{10000, "none", 0, "100"}, {10000, "vat_0", 0, "0"}, {1, "vat_22", 0, "22"}, {3, "vat_22", 1, "22"},
	} {
		got, err := GrossTax(tc.gross, tc.rate)
		if err != nil || got.VAT != tc.vat || got.Rate != tc.wire || !got.Known {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
	}
	if _, err := GrossTax(100, ""); err == nil {
		t.Fatal("unknown VAT silently accepted")
	}
	if _, err := GrossTax(-1, "none"); err == nil {
		t.Fatal("negative gross accepted")
	}
	large, err := GrossTax(math.MaxInt64, "vat_22")
	if err != nil || large.VAT < 0 || large.VAT > large.Gross {
		t.Fatal("tax arithmetic overflow")
	}
	a, _ := GrossTax(12200, "vat_22")
	b, _ := GrossTax(10000, "none")
	a.Add(b)
	if a.Gross != 22200 || a.VAT != 2200 || a.Rate != "" || !a.Known {
		t.Fatal("mixed-rate aggregation lost tax")
	}
}

func TestUncertainReportCannotBeRetriedOrInvalidated(t *testing.T) {
	s := SyncState{Status: SyncError, SubmissionUncertain: true, PayloadHash: "original"}
	if s.Retry() == nil || s.Invalidate() || s.Requeue("changed") || s.Status != SyncError || !s.SubmissionUncertain {
		t.Fatal("uncertainty guard bypassed")
	}
}
