package app_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/jobs"
	"github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/integration/mail/mailfake"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/secretbox"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	t       *testing.T
	d       *dbtest.DB
	clk     *clock.Manual
	svc     *app.Service
	mail    *mailfake.Sender
	files   *storagefake.Storage
	worker  *job.Worker
	ip      netip.Addr
	store   *postgres.Store
	auditDB *audit.Log
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	log := logx.Discard()
	box, err := secretbox.New(bytes.Repeat([]byte{9}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	st := postgres.New(d.Pool, log)
	auditLog := audit.New(d.Pool, clk, log)
	e := &env{t: t, d: d, clk: clk, mail: mailfake.New(), files: storagefake.New(), ip: netip.MustParseAddr("203.0.113.10"),
		store: st, auditDB: auditLog}
	e.svc = app.New(app.Deps{
		Users: st.Users(), AuthCodes: st.AuthCodes(), Sessions: st.Sessions(), Organizations: st.Organizations(),
		Platform: st.Organizations(),
		Legal:    st.LegalDocuments(), Storage: e.files, Sender: e.mail, Sealer: box, Uploads: auditLog,
		Tx: tx.NewRunner(d.Pool, log), Jobs: job.NewQueue(d.Pool, clk, log), Audit: auditLog, Clock: clk, Log: log,
		Config: app.Config{
			LoginCodeTTL: 10 * time.Minute, LoginCodeAttempts: 5, CodesPerEmailHour: 5, CodesPerIPHour: 20,
			SessionTTL: 30 * 24 * time.Hour, AdminSessionTTL: 12 * time.Hour, Pepper: []byte(strings.Repeat("p", 32)),
		},
	})
	e.worker = job.NewWorker(d.Pool, clk, log)
	jobs.Register(e.worker, e.svc)
	return e
}

func (e *env) ctx() context.Context {
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, IP: &e.ip})
}

// code запрашивает код, выполняет задание отправки и возвращает код из письма.
func (e *env) code(email string) string {
	e.t.Helper()
	if _, err := e.svc.RequestLoginCode(e.ctx(), email, &e.ip); err != nil {
		e.t.Fatalf("request code: %v", err)
	}
	if _, err := e.worker.RunOnce(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	m, ok := e.mail.Last(domain.NormalizeEmail(email))
	if !ok {
		e.t.Fatalf("no mail to %s", email)
	}
	return m.Code
}

func (e *env) login(email string) *app.LoginResult {
	e.t.Helper()
	r, err := e.svc.Login(e.ctx(), email, e.code(email), &e.ip, "test")
	if err != nil {
		e.t.Fatalf("login: %v", err)
	}
	return r
}

func (e *env) principal(r *app.LoginResult) app.Principal {
	e.t.Helper()
	p, err := e.svc.Authenticate(e.ctx(), r.SessionToken)
	if err != nil {
		e.t.Fatalf("authenticate: %v", err)
	}
	return *p
}

func (e *env) publish(kind domain.LegalDocumentKind) domain.LegalDocument {
	e.t.Helper()
	d, err := e.svc.PublishLegalDocument(context.Background(), kind, []byte("%PDF-1.7 offer"), start)
	if err != nil {
		e.t.Fatal(err)
	}
	return *d
}

func wantCode(t *testing.T, err error, code apperr.Code) *apperr.Error {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	return e
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t)
	ttl, err := e.svc.RequestLoginCode(e.ctx(), "New@Example.ru", &e.ip)
	if err != nil || ttl != 10*time.Minute {
		t.Fatalf("request: %v %v", ttl, err)
	}
	if e.mail.Count() != 0 {
		t.Fatal("mail sent outside of the job")
	}
	if _, err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, ok := e.mail.Last("new@example.ru")
	if !ok || len(m.Code) != 6 || m.ExpiresIn != 10*time.Minute {
		t.Fatalf("mail: %+v %v", m, ok)
	}
	var payload string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT payload::text FROM job`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("job payload after send = %q, %v", payload, err)
	}

	r, err := e.svc.Login(e.ctx(), "new@example.ru", m.Code, &e.ip, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if !r.NewUser || r.SessionToken == "" || r.CSRFToken == "" || r.Principal.OrganizationID != nil {
		t.Fatalf("login result: %+v", r)
	}
	p, err := e.svc.Authenticate(context.Background(), r.SessionToken)
	if err != nil || p.UserID != r.Principal.UserID {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	// Код одноразовый.
	_, err = e.svc.Login(e.ctx(), "new@example.ru", m.Code, &e.ip, "agent")
	wantCode(t, err, apperr.CodeUnauthorized)

	// Повторный вход того же пользователя — не новый пользователь.
	r2 := e.login("new@example.ru")
	if r2.NewUser || r2.Principal.UserID != r.Principal.UserID {
		t.Fatalf("second login: %+v", r2)
	}

	if err := e.svc.Logout(e.ctx(), *p); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Authenticate(context.Background(), r.SessionToken)
	wantCode(t, err, apperr.CodeUnauthorized)
	if _, err := e.svc.Authenticate(context.Background(), r2.SessionToken); err != nil {
		t.Fatalf("other session closed by logout: %v", err)
	}
}

func TestLoginRejections(t *testing.T) {
	e := newEnv(t)
	code := e.code("a@example.ru")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for range 5 {
		_, err := e.svc.Login(e.ctx(), "a@example.ru", wrong, &e.ip, "")
		wantCode(t, err, apperr.CodeUnauthorized)
	}
	_, err := e.svc.Login(e.ctx(), "a@example.ru", code, &e.ip, "")
	wantCode(t, err, apperr.CodeUnauthorized) // попытки исчерпаны

	code = e.code("b@example.ru")
	e.clk.Advance(10 * time.Minute)
	_, err = e.svc.Login(e.ctx(), "b@example.ru", code, &e.ip, "")
	wantCode(t, err, apperr.CodeUnauthorized) // просрочен

	first := e.code("c@example.ru")
	second := e.code("c@example.ru")
	if first != second {
		_, err = e.svc.Login(e.ctx(), "c@example.ru", first, &e.ip, "")
		wantCode(t, err, apperr.CodeUnauthorized) // новый код гасит прежний
	}
	if _, err := e.svc.Login(e.ctx(), "c@example.ru", second, &e.ip, ""); err != nil {
		t.Fatalf("latest code: %v", err)
	}

	_, err = e.svc.Login(e.ctx(), "nobody@example.ru", "123456", &e.ip, "")
	wantCode(t, err, apperr.CodeUnauthorized)
}

func TestBlockedUser(t *testing.T) {
	e := newEnv(t)
	r := e.login("blocked@example.ru")
	if _, err := e.d.Owner.Exec(context.Background(), `UPDATE "user" SET status = 'blocked'`); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Authenticate(context.Background(), r.SessionToken)
	wantCode(t, err, apperr.CodeUnauthorized)
	_, err = e.svc.Login(e.ctx(), "blocked@example.ru", e.code("blocked@example.ru"), &e.ip, "")
	wantCode(t, err, apperr.CodeForbidden)
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		if _, err := e.svc.RequestLoginCode(e.ctx(), "limit@example.ru", nil); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(time.Minute)
	}
	_, err := e.svc.RequestLoginCode(e.ctx(), "limit@example.ru", nil)
	ae := wantCode(t, err, apperr.CodeRateLimited)
	if ae.Details["scope"] != "email" || ae.RetryAfter != 55*time.Minute {
		t.Fatalf("email limit: %+v retry=%v", ae.Details, ae.RetryAfter)
	}
	e.clk.Advance(56 * time.Minute)
	if _, err := e.svc.RequestLoginCode(e.ctx(), "limit@example.ru", nil); err != nil {
		t.Fatalf("after window: %v", err)
	}

	ip := netip.MustParseAddr("198.51.100.1")
	for i := range 20 {
		if _, err := e.svc.RequestLoginCode(e.ctx(), "u"+uuid.NewString()[:8]+"@example.ru", &ip); err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
	}
	_, err = e.svc.RequestLoginCode(e.ctx(), "fresh@example.ru", &ip)
	if ae := wantCode(t, err, apperr.CodeRateLimited); ae.Details["scope"] != "ip" {
		t.Fatalf("ip limit scope: %v", ae.Details)
	}
}

func TestSendLoginCodeJob(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Повтор задания с затёртой нагрузкой не отправляет второе письмо.
	e.code("once@example.ru")
	if err := e.svc.SendLoginCode(ctx, []byte(`{}`)); err != nil || e.mail.Count() != 1 {
		t.Fatalf("repeat: %v count=%d", err, e.mail.Count())
	}

	// Код просрочен к моменту отправки — письма нет, задание завершается.
	if _, err := e.svc.RequestLoginCode(e.ctx(), "late@example.ru", nil); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(11 * time.Minute)
	if _, err := e.worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.mail.Last("late@example.ru"); ok {
		t.Fatal("expired code was sent")
	}

	// Сбой почты — повтор задания.
	e.mail.FailWith(errors.New("smtp down"))
	if _, err := e.svc.RequestLoginCode(e.ctx(), "retry@example.ru", nil); err != nil {
		t.Fatal(err)
	}
	_, _ = e.worker.RunOnce(ctx)
	e.mail.FailWith(nil)
	e.clk.Advance(30 * time.Second)
	_, _ = e.worker.RunOnce(ctx)
	if _, ok := e.mail.Last("retry@example.ru"); !ok {
		t.Fatal("code was not sent after retry")
	}
}

func TestOrganizations(t *testing.T) {
	e := newEnv(t)
	p := e.principal(e.login("adv@example.ru"))
	_, err := e.svc.GetOrganization(e.ctx(), p)
	wantCode(t, err, apperr.CodeNotFound)

	in := accounttest.AdvertiserLegalEntity()
	o, err := e.svc.CreateOrganization(e.ctx(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Advertiser || o.Blogger != nil {
		t.Fatalf("created: %+v", o)
	}

	// ИНН занят другой организацией.
	p2 := e.principal(e.login("other@example.ru"))
	other := in
	other.Cabinet = domain.CabinetAdvertiser
	_, err = e.svc.CreateOrganization(e.ctx(), p2, other)
	wantCode(t, err, apperr.CodeINNTaken)

	// Второй кабинет: ИНН должен совпадать, поля блогера обязательны.
	blogger := in
	blogger.Cabinet = domain.CabinetBlogger
	_, err = e.svc.CreateOrganization(e.ctx(), p, blogger)
	if ae := wantCode(t, err, apperr.CodeValidationFailed); ae.Details["field"] != "tax_regime" {
		t.Fatalf("missing blogger fields: %v", ae.Details)
	}
	wrongINN := blogger
	wrongINN.INN = accounttest.INN(domain.LegalEntity)
	_, err = e.svc.CreateOrganization(e.ctx(), p, wrongINN)
	if ae := wantCode(t, err, apperr.CodeValidationFailed); ae.Details["reason"] != domain.ReasonImmutable {
		t.Fatalf("inn mismatch: %v", ae.Details)
	}
	blogger.Fields = accounttest.BloggerFields()
	blogger.Fields.SignerBasisName, blogger.Fields.SignerBasisNumber = nil, nil
	o, err = e.svc.CreateOrganization(e.ctx(), p, blogger)
	if err != nil || o.Blogger == nil || o.Name != *in.Fields.Name {
		t.Fatalf("add cabinet: %+v %v", o, err)
	}
	_, err = e.svc.CreateOrganization(e.ctx(), p, blogger)
	wantCode(t, err, apperr.CodeConflict)

	phone := "+79995554433"
	o, err = e.svc.UpdateOrganization(e.ctx(), p, domain.Fields{Phone: &phone})
	if err != nil || o.Phone != phone {
		t.Fatalf("update: %v", err)
	}
	var data string
	err = e.d.Pool.QueryRow(context.Background(),
		`SELECT data::text FROM audit_log WHERE action = 'organization.update'`).Scan(&data)
	if err != nil || !strings.Contains(data, "+7******4433") || strings.Contains(data, phone) {
		t.Fatalf("audit data %q %v", data, err)
	}
	var actions int
	_ = e.d.Pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT action) FROM audit_log WHERE action IN ('organization.create','organization.add_cabinet','organization.update','auth.login')`).Scan(&actions)
	if actions != 4 {
		t.Fatalf("audit actions recorded: %d", actions)
	}
}

func TestTaxDocument(t *testing.T) {
	e := newEnv(t)
	adv := e.principal(e.login("adv@example.ru"))
	if _, err := e.svc.CreateOrganization(e.ctx(), adv, accounttest.AdvertiserLegalEntity()); err != nil {
		t.Fatal(err)
	}
	adv = e.principal(e.login("adv@example.ru"))
	_, err := e.svc.UploadTaxDocument(e.ctx(), adv, []byte("%PDF-1.7"))
	wantCode(t, err, apperr.CodeForbidden)

	blg := e.principal(e.login("blg@example.ru"))
	if _, err := e.svc.CreateOrganization(e.ctx(), blg, accounttest.BloggerSelfEmployed()); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UploadTaxDocument(e.ctx(), blg, []byte("GIF89a not allowed"))
	if ae := wantCode(t, err, apperr.CodeValidationFailed); ae.Details["reason"] != "unsupported_type" {
		t.Fatalf("type: %v", ae.Details)
	}
	_, err = e.svc.UploadTaxDocument(e.ctx(), blg, make([]byte, app.MaxTaxDocumentSize+1))
	wantCode(t, err, apperr.CodeValidationFailed)

	o, err := e.svc.UploadTaxDocument(e.ctx(), blg, []byte("%PDF-1.7 tax document"))
	if err != nil || o.TaxDocument == nil || !strings.HasSuffix(o.TaxDocument.StorageKey, ".pdf") {
		t.Fatalf("upload: %+v %v", o, err)
	}
	if obj, ok := e.files.Object(o.TaxDocument.StorageKey); !ok || obj.ContentType != "application/pdf" {
		t.Fatalf("stored object: %+v %v", obj, ok)
	}
}

func TestLegalDocuments(t *testing.T) {
	e := newEnv(t)
	advOffer := e.publish(domain.DocAdvertiserOffer)
	blgOffer := e.publish(domain.DocBloggerOffer)
	rules := e.publish(domain.DocPlatformRules)
	privacy := e.publish(domain.DocPrivacyPolicy)

	p := e.principal(e.login("adv@example.ru"))
	err := e.svc.AcceptLegalDocuments(e.ctx(), p, []uuid.UUID{rules.ID}, &e.ip)
	wantCode(t, err, apperr.CodeForbidden) // без организации

	if _, err := e.svc.CreateOrganization(e.ctx(), p, accounttest.AdvertiserLegalEntity()); err != nil {
		t.Fatal(err)
	}
	p = e.principal(e.login("adv@example.ru"))
	me, err := e.svc.GetMe(e.ctx(), p)
	if err != nil || len(me.PendingDocuments) != 3 {
		t.Fatalf("pending: %+v %v", me, err)
	}

	err = e.svc.AcceptLegalDocuments(e.ctx(), p, []uuid.UUID{blgOffer.ID}, &e.ip)
	wantCode(t, err, apperr.CodeValidationFailed) // документ не для этого кабинета
	err = e.svc.AcceptLegalDocuments(e.ctx(), p, []uuid.UUID{ids.New()}, &e.ip)
	wantCode(t, err, apperr.CodeNotFound)

	all := []uuid.UUID{advOffer.ID, rules.ID, privacy.ID}
	for range 2 {
		if err := e.svc.AcceptLegalDocuments(e.ctx(), p, all, &e.ip); err != nil {
			t.Fatal(err)
		}
	}
	if me, _ = e.svc.GetMe(e.ctx(), p); len(me.PendingDocuments) != 0 {
		t.Fatalf("still pending: %+v", me.PendingDocuments)
	}
	list, err := e.svc.ListLegalDocuments(e.ctx(), p)
	if err != nil || len(list) != 4 {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, v := range list {
		if v.URL == "" || v.Accepted != (v.Document.Kind != domain.DocBloggerOffer) {
			t.Fatalf("view %+v", v)
		}
	}

	// Новая версия требует нового принятия (A-25); старая больше не принимается.
	v2 := e.publish(domain.DocAdvertiserOffer)
	if me, _ = e.svc.GetMe(e.ctx(), p); len(me.PendingDocuments) != 1 || me.PendingDocuments[0].ID != v2.ID || v2.Version != 2 {
		t.Fatalf("after new version: %+v", me.PendingDocuments)
	}
	err = e.svc.AcceptLegalDocuments(e.ctx(), p, []uuid.UUID{advOffer.ID}, &e.ip)
	wantCode(t, err, apperr.CodeNotFound)
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	r := e.login("s@example.ru")
	e.clk.Advance(29 * 24 * time.Hour)
	if _, err := e.svc.Authenticate(context.Background(), r.SessionToken); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	// Использование продлило сессию на 30 суток.
	e.clk.Advance(29 * 24 * time.Hour)
	if _, err := e.svc.Authenticate(context.Background(), r.SessionToken); err != nil {
		t.Fatalf("after extension: %v", err)
	}
	e.clk.Advance(31 * 24 * time.Hour)
	_, err := e.svc.Authenticate(context.Background(), r.SessionToken)
	wantCode(t, err, apperr.CodeUnauthorized)
}
