package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

func TestClassifyOrdStatus(t *testing.T) {
	cases := []struct {
		status string
		erir   bool
		want   Progress
	}{
		{OrdRequestAccepted, true, ProgressInWork},
		{OrdSuccess, true, ProgressAccepted},
		{OrdErirSyncSuccess, true, ProgressAccepted},
		{OrdErirAsyncSuccess, true, ProgressFinalSuccess},
		{OrdError, true, ProgressFinalError},
		{OrdRejected, true, ProgressFinalError},
		{OrdErirSyncError, true, ProgressFinalError},
		{OrdErirAsyncError, true, ProgressFinalError},
		{"something new", true, ProgressInWork},
		// Площадка: ЕРИР не подтверждает (R-3, Q-40).
		{OrdSuccess, false, ProgressFinalSuccess},
		{OrdRequestAccepted, false, ProgressInWork},
		{OrdErirAsyncSuccess, false, ProgressFinalSuccess},
		{OrdError, false, ProgressFinalError},
	}
	for _, c := range cases {
		if got := ClassifyOrdStatus(c.status, c.erir); got != c.want {
			t.Errorf("ClassifyOrdStatus(%q, erir=%v) = %v, want %v", c.status, c.erir, got, c.want)
		}
	}
	if PassesErir(TypePlatform) || !PassesErir(TypeCreative) {
		t.Fatal("PassesErir: only platform skips ERIR")
	}
}

func TestSyncTransitions(t *testing.T) {
	s := NewSyncState()
	if s.Ready() || s.UpToDate("h") {
		t.Fatal("pending object is ready or up to date")
	}
	s.MarkSent("ext", "req-1", OrdRequestAccepted, "h", t0)
	if s.Status != SyncSent || s.Attempts != 1 || !s.SentAt.Equal(t0) || s.RequestID != "req-1" {
		t.Fatalf("after MarkSent: %+v", s)
	}
	if s.Ready() {
		t.Fatal("sent + Request accepted by ORD must not be ready")
	}
	if o := s.ApplyStatus(OrdSuccess, nil, true, t0.Add(time.Minute)); o != OutcomeWaiting || s.Status != SyncSent {
		t.Fatalf("ORD success: outcome %v, status %s", o, s.Status)
	}
	if !s.Ready() {
		t.Fatal("sent + ORD success must be ready (R-2)")
	}
	if o := s.ApplyStatus(OrdErirAsyncSuccess, nil, true, t0.Add(2*time.Minute)); o != OutcomeSynced || s.Status != SyncSynced {
		t.Fatalf("ERIR async success: outcome %v, status %s", o, s.Status)
	}
	if !s.UpToDate("h") || s.UpToDate("h2") {
		t.Fatal("synced is up to date only with the same hash")
	}
	if err := s.Retry(); err == nil {
		t.Fatal("Retry from synced must be refused")
	}
	if !s.Requeue("h2") || s.Status != SyncPending {
		t.Fatalf("Requeue with a new hash: %+v", s)
	}
	if s.Requeue("h3") {
		t.Fatal("Requeue of a pending object changed state")
	}

	s.MarkSent("ext", "req-2", OrdRequestAccepted, "h2", t0)
	errs := []string{"inn: invalid", "name: required"}
	if o := s.ApplyStatus(OrdErirAsyncError, errs, true, t0); o != OutcomeFailed || s.Status != SyncError {
		t.Fatalf("ERIR async error: outcome %v, status %s", o, s.Status)
	}
	if s.Error != "inn: invalid; name: required" {
		t.Fatalf("sync error %q", s.Error)
	}
	if err := s.Retry(); err != nil || s.Status != SyncPending || s.Error != "" {
		t.Fatalf("Retry from error: %v, %+v", err, s)
	}
	s.Fail(strings.Repeat("я", 3000), true)
	if len([]rune(s.Error)) != maxSyncError || s.Attempts != 3 {
		t.Fatalf("Fail: error %d runes, attempts %d", len([]rune(s.Error)), s.Attempts)
	}
	var se *StateError
	s.Status = SyncSent
	if err := s.Retry(); !errors.As(err, &se) {
		t.Fatalf("Retry from sent: %v", err)
	}
}

func TestNextPoll(t *testing.T) {
	r := PollRules{}.WithDefaults()
	cases := []struct {
		after   time.Duration
		delay   time.Duration
		timeout bool
	}{
		{0, time.Minute, false},
		{9*time.Minute + 59*time.Second, time.Minute, false},
		{10 * time.Minute, 10 * time.Minute, false},
		{23*time.Hour + 55*time.Minute, 5 * time.Minute, false},
		{24 * time.Hour, 0, true},
		{25 * time.Hour, 0, true},
	}
	for _, c := range cases {
		d, timeout := NextPoll(t0, t0.Add(c.after), r)
		if d != c.delay || timeout != c.timeout {
			t.Errorf("NextPoll after %v = (%v, %v), want (%v, %v)", c.after, d, timeout, c.delay, c.timeout)
		}
	}
}

func TestObjectRef(t *testing.T) {
	id := uuid.MustParse("0190a000-0000-7000-8000-000000000001")
	ref := ObjectRef{Type: TypeContract, ID: id}
	got, err := ParseObjectRef(ref.String())
	if err != nil || got != ref {
		t.Fatalf("ParseObjectRef(%s) = %v, %v", ref, got, err)
	}
	for _, bad := range []string{"contract", "act:" + id.String(), "creative:xyz"} {
		if _, err := ParseObjectRef(bad); err == nil {
			t.Errorf("ParseObjectRef(%q) accepted", bad)
		}
	}
}

func TestReceiptFor(t *testing.T) {
	r := Receipt{RequestID: "r", Status: OrdRequestAccepted, Token: "common", Errors: []string{"general"},
		ObjectStatuses: []ObjectStatus{{ObjectID: "a", Status: OrdErirSyncSuccess, Token: "tok-a", Errors: []string{"own"}}}}
	st, tok, errs := r.For("a")
	if st != OrdRequestAccepted || tok != "tok-a" || len(errs) != 2 {
		t.Fatalf("For(a) = %q %q %v", st, tok, errs)
	}
	st, tok, _ = r.For("b")
	if st != OrdRequestAccepted || tok != "common" {
		t.Fatalf("For(b) = %q %q", st, tok)
	}
	r.Status = OrdErirAsyncSuccess
	r.ObjectStatuses = []ObjectStatus{{ObjectID: "a", Status: OrdErirAsyncSuccess}, {ObjectID: "other", Status: OrdRequestAccepted}}
	if st, _, _ := r.For("a"); st != OrdRequestAccepted {
		t.Fatal("pending child hidden by matching success")
	}
	r.ObjectStatuses = append(r.ObjectStatuses, ObjectStatus{ObjectID: "other", Status: OrdErirAsyncError})
	st, _, _ = r.For("a")
	if st != OrdErirAsyncError {
		t.Fatal("child failure hidden by top-level success")
	}
}

func TestPayloadHash(t *testing.T) {
	a, _ := PayloadHash(json.RawMessage(`{"id":"1","name":"А","isRr":false}`))
	b, _ := PayloadHash(json.RawMessage(`{"isRr":false, "name":"А","id":"1"}`))
	c, _ := PayloadHash(json.RawMessage(`{"id":"1","name":"Б","isRr":false}`))
	if a != b {
		t.Fatal("hash depends on key order")
	}
	if a == c {
		t.Fatal("hash does not change with name")
	}
}
