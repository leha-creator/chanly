package postgres_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*postgres.Store, *dbtest.DB) {
	d := dbtest.New(t)
	return postgres.New(d.Pool, logx.Discard()), d
}

func createUser(t *testing.T, st *postgres.Store, email string) *domain.User {
	t.Helper()
	u := domain.NewUser(ids.New(), email, now)
	if err := st.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsers(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	u := createUser(t, st, "Ivan@Example.ru")

	got, err := st.Users().GetByEmail(ctx, "ivan@EXAMPLE.ru")
	if err != nil || got.ID != u.ID || got.Status != domain.StatusActive || got.Email != "ivan@example.ru" {
		t.Fatalf("GetByEmail: %+v %v", got, err)
	}
	if _, err := st.Users().GetByEmail(ctx, "none@example.ru"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	login := now.Add(time.Hour)
	if err := st.Users().SetLastLogin(ctx, u.ID, login); err != nil {
		t.Fatal(err)
	}
	got, err = st.Users().GetByID(ctx, u.ID)
	if err != nil || got.LastLoginAt == nil || !got.LastLoginAt.Equal(login) {
		t.Fatalf("GetByID: %+v %v", got, err)
	}
}

func TestAuthCodes(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	codes := st.AuthCodes()
	pepper := []byte("pepper")
	ip := netip.MustParseAddr("203.0.113.5")
	other := netip.MustParseAddr("203.0.113.6")

	first := domain.NewLoginCode(ids.New(), "a@example.ru", "111111", pepper, &ip, 10*time.Minute, now)
	second := domain.NewLoginCode(ids.New(), "a@example.ru", "222222", pepper, &other, 10*time.Minute, now.Add(time.Minute))
	third := domain.NewLoginCode(ids.New(), "b@example.ru", "333333", pepper, &ip, 10*time.Minute, now.Add(2*time.Minute))
	for _, c := range []*domain.LoginCode{first, second, third} {
		if err := codes.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	c, err := codes.LatestActiveForUpdate(ctx, "A@example.ru")
	if err != nil || c.ID != second.ID || c.Email != "a@example.ru" || *c.RequestedIP != other {
		t.Fatalf("latest: %+v %v", c, err)
	}
	wc, err := codes.CountByEmailSince(ctx, "a@example.ru", now.Add(-time.Hour))
	if err != nil || wc.Count != 2 || !wc.Oldest.Equal(now) {
		t.Fatalf("by email: %+v %v", wc, err)
	}
	wc, err = codes.CountByIPSince(ctx, ip, now.Add(-time.Hour))
	if err != nil || wc.Count != 2 {
		t.Fatalf("by ip: %+v %v", wc, err)
	}
	wc, err = codes.CountByIPSince(ctx, ip, now.Add(time.Hour))
	if err != nil || wc.Count != 0 {
		t.Fatalf("empty window: %+v %v", wc, err)
	}

	c.Attempts = 2
	c.UpdatedAt = now.Add(3 * time.Minute)
	if err := codes.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c, _ = codes.LatestActiveForUpdate(ctx, "a@example.ru"); c.Attempts != 2 {
		t.Fatalf("attempts not saved: %+v", c)
	}
	if err := codes.ConsumeActive(ctx, "a@example.ru", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := codes.LatestActiveForUpdate(ctx, "a@example.ru"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("codes are still active: %v", err)
	}
	if _, err := codes.LatestActiveForUpdate(ctx, "b@example.ru"); err != nil {
		t.Fatalf("other address affected: %v", err)
	}
}

func TestSessions(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	u := createUser(t, st, "s@example.ru")
	_, hash, _ := domain.NewToken()
	ip := netip.MustParseAddr("2001:db8::1")
	ua := "test-agent"
	s := domain.NewSession(ids.New(), u.ID, hash, &ip, &ua, time.Hour, now)
	if err := st.Sessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := st.Sessions().GetByTokenHash(ctx, hash)
	if err != nil || got.ID != s.ID || *got.IP != ip || *got.UserAgent != ua || !got.Active(now) {
		t.Fatalf("get: %+v %v", got, err)
	}
	later := now.Add(2 * time.Hour)
	if err := st.Sessions().SetExpiresAt(ctx, s.ID, later, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Sessions().Revoke(ctx, s.ID, now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Sessions().GetByTokenHash(ctx, hash)
	if !got.ExpiresAt.Equal(later) || got.RevokedAt == nil || got.Active(now) {
		t.Fatalf("after revoke: %+v", got)
	}
	if _, err := st.Sessions().GetByTokenHash(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestOrganizations(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	orgs := st.Organizations()
	u := createUser(t, st, "org@example.ru")

	if _, err := orgs.GetByUser(ctx, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no organization yet: %v", err)
	}
	o, err := domain.NewOrganization(ids.New(), accounttest.AdvertiserLegalEntity(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := orgs.Create(ctx, o, u.ID); err != nil {
		t.Fatal(err)
	}
	got, err := orgs.GetByUser(ctx, u.ID)
	if err != nil || got.ID != o.ID || !got.Advertiser || got.Blogger != nil || *got.Address != *o.Address {
		t.Fatalf("by user: %+v %v", got, err)
	}
	if taken, err := orgs.INNTaken(ctx, o.INN); err != nil || !taken {
		t.Fatalf("inn taken: %v %v", taken, err)
	}

	// Второй кабинет и документ — в транзакции с блокировкой строки.
	runner := tx.NewRunner(d.Pool, logx.Discard())
	err = runner.InTx(ctx, func(ctx context.Context) error {
		o, err := orgs.GetForUpdate(ctx, o.ID)
		if err != nil {
			return err
		}
		if err := o.AddCabinet(domain.CabinetBlogger, accounttest.BloggerFields(), now.Add(time.Hour)); err != nil {
			return err
		}
		if err := o.SetTaxDocument(domain.FileRef{StorageKey: "tax_document/x", SHA256: "abc"}, now.Add(time.Hour)); err != nil {
			return err
		}
		return orgs.Update(ctx, o)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = orgs.GetByUser(ctx, u.ID)
	if got.Blogger == nil || *got.TaxRegime != domain.TaxOSNO || got.TaxDocument == nil || got.TaxDocument.SHA256 != "abc" ||
		*got.SignerBasisNumber != "1" || !got.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("after update: %+v", got)
	}

	// Второй организации у пользователя быть не может (A-13).
	o2, _ := domain.NewOrganization(ids.New(), accounttest.BloggerSelfEmployed(), now)
	if err := orgs.Create(ctx, o2, u.ID); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("second organization: %v", err)
	}
}

func TestINNRace(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	in := accounttest.AdvertiserLegalEntity()
	runner := tx.NewRunner(d.Pool, logx.Discard())
	users := []*domain.User{createUser(t, st, "r1@example.ru"), createUser(t, st, "r2@example.ru")}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := domain.NewOrganization(ids.New(), in, now)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = runner.InTx(ctx, func(ctx context.Context) error { return st.Organizations().Create(ctx, o, u.ID) })
		}()
	}
	wg.Wait()
	ok, taken := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case apperr.Is(err, apperr.CodeINNTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || taken != 1 {
		t.Fatalf("ok=%d inn_taken=%d", ok, taken)
	}
}

func TestLegalDocuments(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	legal := st.LegalDocuments()
	u := createUser(t, st, "legal@example.ru")
	o, _ := domain.NewOrganization(ids.New(), accounttest.AdvertiserLegalEntity(), now)
	if err := st.Organizations().Create(ctx, o, u.ID); err != nil {
		t.Fatal(err)
	}

	if v, err := legal.LatestVersion(ctx, domain.DocAdvertiserOffer); err != nil || v != 0 {
		t.Fatalf("no versions: %d %v", v, err)
	}
	newDoc := func(kind domain.LegalDocumentKind, version int) *domain.LegalDocument {
		d := &domain.LegalDocument{ID: ids.New(), Kind: kind, Version: version, PublishedAt: now,
			StorageKey: "legal/" + uuid.NewString(), SHA256: "sum", CreatedAt: now}
		if err := legal.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	v1 := newDoc(domain.DocAdvertiserOffer, 1)
	v2 := newDoc(domain.DocAdvertiserOffer, 2)
	rules := newDoc(domain.DocPlatformRules, 1)
	if err := legal.Create(ctx, &domain.LegalDocument{ID: ids.New(), Kind: domain.DocPlatformRules, Version: 1,
		PublishedAt: now, StorageKey: "k", SHA256: "s", CreatedAt: now}); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("duplicate version: %v", err)
	}

	current, err := legal.Current(ctx)
	if err != nil || len(current) != 2 {
		t.Fatalf("current: %+v %v", current, err)
	}
	for _, d := range current {
		if d.Kind == domain.DocAdvertiserOffer && d.ID != v2.ID {
			t.Fatalf("current advertiser offer is %v, want version 2", d.Version)
		}
	}
	ip := netip.MustParseAddr("203.0.113.9")
	for range 2 { // повторное принятие не создаёт дубль и не падает
		for _, d := range []*domain.LegalDocument{v1, rules} {
			if err := legal.Accept(ctx, domain.LegalAcceptance{ID: ids.New(), LegalDocumentID: d.ID, UserID: u.ID,
				OrganizationID: o.ID, AcceptedAt: now, IP: &ip}); err != nil {
				t.Fatal(err)
			}
		}
	}
	accepted, err := legal.AcceptedIDs(ctx, o.ID)
	if err != nil || len(accepted) != 2 || !accepted[v1.ID] || accepted[v2.ID] {
		t.Fatalf("accepted: %v %v", accepted, err)
	}
	if v, _ := legal.LatestVersion(ctx, domain.DocAdvertiserOffer); v != 2 {
		t.Fatalf("latest version %d", v)
	}
}
