package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var (
	t0         = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	placement  = uuid.MustParse("01900000-0000-7000-8000-000000000001")
	org        = uuid.MustParse("01900000-0000-7000-8000-000000000002")
	contractor = uuid.MustParse("01900000-0000-7000-8000-000000000003")
	admin      = uuid.MustParse("01900000-0000-7000-8000-000000000004")
)

func newPayout(t *testing.T, status Status) Payout {
	t.Helper()
	p, err := NewPayout(uuid.New(), placement, org, MethodRocketwork, contractor, 100000, t0)
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case StatusSent:
		_, err = p.MarkSent("rw-1", t0)
	case StatusSucceeded:
		_, err = p.MarkSucceeded(3000, "https://r/1", "", t0)
	case StatusFailed:
		_, err = p.MarkFailed(FailureTaxStatusLost, "lost", t0)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPayout(t *testing.T) {
	cases := []struct {
		name       string
		method     Method
		contractor uuid.UUID
		amount     money.Kopecks
		ok         bool
	}{
		{"rocketwork", MethodRocketwork, contractor, 1, true},
		{"bank", MethodBankTransfer, uuid.Nil, 1, true},
		{"rocketwork without contractor", MethodRocketwork, uuid.Nil, 1, false},
		{"bank with contractor", MethodBankTransfer, contractor, 1, false},
		{"zero amount", MethodRocketwork, contractor, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := NewPayout(uuid.New(), placement, org, c.method, c.contractor, c.amount, t0)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if c.ok && (p.Status != StatusCreated || p.Attempt != 1 || p.IdempotencyKey != "rw-payout:"+p.ID.String()) {
				t.Fatalf("unexpected payout %+v", p)
			}
		})
	}
}

func TestPayoutTransitions(t *testing.T) {
	type op func(p *Payout) (bool, error)
	sent := func(id string) op { return func(p *Payout) (bool, error) { return p.MarkSent(id, t0) } }
	succ := func(fee money.Kopecks) op {
		return func(p *Payout) (bool, error) { return p.MarkSucceeded(fee, "https://r/1", "", t0) }
	}
	fail := func(c FailureCode) op { return func(p *Payout) (bool, error) { return p.MarkFailed(c, "m", t0) } }
	cases := []struct {
		name    string
		from    Status
		op      op
		changed bool
		err     error
		want    Status
	}{
		{"created → sent", StatusCreated, sent("rw-1"), true, nil, StatusSent},
		{"sent → sent same id", StatusSent, sent("rw-1"), false, nil, StatusSent},
		{"sent → sent other id", StatusSent, sent("rw-2"), false, ErrInvalidPayoutState, StatusSent},
		{"sent empty id", StatusCreated, sent(""), false, ErrInvalidPayout, StatusCreated},
		{"created → succeeded", StatusCreated, succ(3000), true, nil, StatusSucceeded},
		{"sent → succeeded", StatusSent, succ(3000), true, nil, StatusSucceeded},
		{"succeeded again same", StatusSucceeded, succ(3000), false, nil, StatusSucceeded},
		{"succeeded again other fee", StatusSucceeded, succ(1), false, ErrInvalidPayoutState, StatusSucceeded},
		{"failed → succeeded", StatusFailed, succ(3000), false, ErrInvalidPayoutState, StatusFailed},
		{"sent → failed", StatusSent, fail(FailureProviderError), true, nil, StatusFailed},
		{"created → failed", StatusCreated, fail(FailureContractorNotActive), true, nil, StatusFailed},
		{"failed again same code", StatusFailed, fail(FailureTaxStatusLost), false, nil, StatusFailed},
		{"failed again other code", StatusFailed, fail(FailureProviderError), false, ErrInvalidPayoutState, StatusFailed},
		{"succeeded → failed", StatusSucceeded, fail(FailureProviderError), false, ErrInvalidPayoutState, StatusSucceeded},
		{"unknown code", StatusSent, fail("bad"), false, ErrInvalidPayout, StatusSent},
		{"succeeded → sent", StatusSucceeded, sent("rw-1"), false, ErrInvalidPayoutState, StatusSucceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPayout(t, c.from)
			changed, err := c.op(&p)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if changed != c.changed || p.Status != c.want {
				t.Fatalf("changed=%v status=%s, want %v %s", changed, p.Status, c.changed, c.want)
			}
		})
	}
}

func TestSucceededFromCreatedSetsSentAt(t *testing.T) {
	p := newPayout(t, StatusCreated)
	if _, err := p.MarkSucceeded(0, "", "", t0); err != nil {
		t.Fatal(err)
	}
	if p.SentAt == nil || p.CompletedAt == nil {
		t.Fatalf("sent_at %v completed_at %v", p.SentAt, p.CompletedAt)
	}
}

func TestNextAttempt(t *testing.T) {
	failed := newPayout(t, StatusFailed)
	next, err := failed.NextAttempt(uuid.New(), contractor, admin, t0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt != 2 || next.Status != StatusCreated || next.IdempotencyKey == failed.IdempotencyKey || next.CreatedBy != admin {
		t.Fatalf("next attempt %+v", next)
	}
	for _, s := range []Status{StatusCreated, StatusSent, StatusSucceeded} {
		p := newPayout(t, s)
		if _, err := p.NextAttempt(uuid.New(), contractor, admin, t0); !errors.Is(err, ErrPayoutNotRetryable) {
			t.Errorf("%s: err = %v", s, err)
		}
	}
	bank, _ := NewPayout(uuid.New(), placement, org, MethodBankTransfer, uuid.Nil, 1, t0)
	_, _ = bank.MarkFailed(FailureProviderError, "", t0)
	if _, err := bank.NextAttempt(uuid.New(), uuid.Nil, admin, t0); !errors.Is(err, ErrPayoutNotRetryable) {
		t.Errorf("bank transfer retry: %v", err)
	}
}

func TestFailureCodes(t *testing.T) {
	cases := []struct {
		code      FailureCode
		auto, adm bool
	}{
		{FailureContractorNotActive, false, false},
		{FailureTaxStatusLost, false, true},
		{FailureIncomeLimitExceeded, false, true},
		{FailureInsufficientDeposit, true, true}, // отказ сервиса после sent: деньги ждут пополнения
		{FailureProviderError, true, true},
	}
	for _, c := range cases {
		if !c.code.Valid() || c.code.AutoRetried() != c.auto || c.code.NeedsAdmin() != c.adm {
			t.Errorf("%s: valid=%v auto=%v admin=%v", c.code, c.code.Valid(), c.code.AutoRetried(), c.code.NeedsAdmin())
		}
	}
	if FailureCode("x").Valid() {
		t.Error("unknown code is valid")
	}
}

func TestContractor(t *testing.T) {
	if ContractorRequired(LegalEntity) || !ContractorRequired(SelfEmployed) || !ContractorRequired(SoleProprietor) {
		t.Fatal("ContractorRequired")
	}
	if MethodFor(LegalEntity) != MethodBankTransfer || MethodFor(SelfEmployed) != MethodRocketwork {
		t.Fatal("MethodFor")
	}
	c := NewContractor(contractor, org, t0)
	if c.CanAccept() || c.Registered() || !c.Stale(t0, time.Hour) {
		t.Fatalf("new contractor %+v", c)
	}
	steps := []struct {
		in      ProviderContractor
		changed bool
		want    ContractorStatus
		reason  string
	}{
		{ProviderContractor{ExternalID: "c-1", Status: ContractorInvited, OnboardingURL: "https://o/1"}, false, ContractorInvited, ""},
		{ProviderContractor{Status: ContractorPending}, true, ContractorPending, ""},
		{ProviderContractor{Status: ContractorActive}, true, ContractorActive, ""},
		{ProviderContractor{Status: ContractorActive}, false, ContractorActive, ""},
		{ProviderContractor{Status: ContractorBlocked, BlockedReason: "tax_status_lost"}, true, ContractorBlocked, "tax_status_lost"},
		{ProviderContractor{Status: ContractorActive}, true, ContractorActive, ""},
		{ProviderContractor{Status: "weird"}, false, ContractorActive, ""},
	}
	for i, s := range steps {
		if changed := c.Apply(s.in, t0.Add(time.Duration(i)*time.Minute)); changed != s.changed || c.Status != s.want || c.BlockedReason != s.reason {
			t.Fatalf("step %d: changed=%v status=%s reason=%q", i, changed, c.Status, c.BlockedReason)
		}
	}
	if c.ExternalID != "c-1" || c.OnboardingURL != "https://o/1" || !c.CanAccept() || c.Stale(t0.Add(10*time.Minute), time.Hour) {
		t.Fatalf("contractor %+v", c)
	}
}

func TestPollDelay(t *testing.T) {
	fast, slow := 5*time.Minute, time.Hour
	for _, c := range []struct {
		minutes int
		want    time.Duration
	}{{0, fast}, {59, fast}, {60, slow}, {61, slow}} {
		if got := PollDelay(t0, t0.Add(time.Duration(c.minutes)*time.Minute), fast, slow); got != c.want {
			t.Errorf("%d min: %v, want %v", c.minutes, got, c.want)
		}
	}
}

func TestLimits(t *testing.T) {
	limit := money.Kopecks(240000000)
	for _, c := range []struct {
		year, amount money.Kopecks
		want         bool
	}{
		{215990000, 0, false},      // 89,996 %
		{215000000, 999999, false}, // 89,9999 %
		{215000000, 1000000, true}, // ровно 90 %
		{240000000, 0, true},       // 100 %
		{0, 0, false},
	} {
		if got := NPDWarning(c.year, c.amount, limit); got != c.want {
			t.Errorf("NPDWarning(%d,%d) = %v", c.year, c.amount, got)
		}
	}
	if NPDWarning(1, 1, 0) {
		t.Error("zero limit warns")
	}
	if DepositNeed(100, 50) != 150 {
		t.Error("DepositNeed")
	}
	if DepositMismatch(1000, 900, 100) || !DepositMismatch(1000, 899, 100) || !DepositMismatch(899, 1000, 100) {
		t.Error("DepositMismatch")
	}
}

func TestYearBounds(t *testing.T) {
	// 31 декабря 21:30 UTC — уже 1 января по Москве.
	from, to := YearBounds(time.Date(2026, 12, 31, 21, 30, 0, 0, time.UTC))
	if !from.Equal(time.Date(2026, 12, 31, 21, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2027, 12, 31, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("bounds %v %v", from, to)
	}
	from, _ = YearBounds(time.Date(2026, 12, 31, 20, 59, 0, 0, time.UTC))
	if from.Year() != 2025 {
		t.Fatalf("from %v", from)
	}
}
