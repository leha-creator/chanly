package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/markingtest"
)

// initialChain — изначальная цепочка рекламодателя org в отдельной транзакции.
func (e *env) initialChain(org uuid.UUID) {
	e.t.Helper()
	if err := e.svc.Tx.InTx(context.Background(), func(ctx context.Context) error {
		_, err := e.svc.EnsureInitialChain(ctx, org)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) publisherChain(org uuid.UUID) {
	e.t.Helper()
	if err := e.svc.Tx.InTx(context.Background(), func(ctx context.Context) error {
		_, err := e.svc.EnsurePublisherChain(ctx, org)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestAdvertiserOrganizationAndInitialContract(t *testing.T) {
	e := newEnv(t)
	adv := markingtest.Organization(t, e.d.Pool, accountdomain.SoleProprietor, "ИП Рекламодатель")
	markingtest.Accept(t, e.d.Pool, adv, markingtest.AdvertiserOffer, time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC))
	e.initialChain(adv)
	e.run()

	o := e.org(adv)
	if o.Sync.Status != domain.SyncSent || o.IsRr || o.Sync.ExternalID != adv.String() {
		t.Fatalf("advertiser: %+v", o)
	}
	obj, ok := e.ord.Object(adv.String())
	var body ord.Organization
	if !ok || obj.Decode(&body) != nil || body.Type != "ip" || body.IsRr || body.IsOrs || body.KPP != "" {
		t.Fatalf("advertiser request: %+v %s", body, obj.Body)
	}
	if e.jobStatus(app.PollDedupeKey(o.Ref())) != "pending" {
		t.Fatal("status poll is not scheduled")
	}
	// Договор ждёт, пока стороны не приняты ОРД (R-2).
	contract := e.contract(domain.ContractInitial, adv, e.platform)
	if contract.Sync.Status != domain.SyncPending || e.ord.Calls(ord.OpContract) != 0 {
		t.Fatalf("contract sent before parties are ready: %+v", contract.Sync)
	}

	// Повтор отправки уже отправленного объекта второго вызова не делает.
	before := e.ord.Calls(ord.OpOrganization)
	if err := e.svc.Sync(context.Background(), o.Ref(), 1); err != nil {
		t.Fatal(err)
	}
	if e.ord.Calls(ord.OpOrganization) != before {
		t.Fatal("sent object was submitted again")
	}

	// Опрос: ORD success у обеих сторон — договор уходит с датой принятия оферты по Москве.
	e.settle(2)
	contract = e.contract(domain.ContractInitial, adv, e.platform)
	if contract.Sync.Status == domain.SyncPending {
		t.Fatalf("contract still pending: %+v", contract.Sync)
	}
	var ct ord.Contract
	obj, _ = e.ord.Object(contract.ID.String())
	if err := obj.Decode(&ct); err != nil {
		t.Fatal(err)
	}
	if ct.Date != "2026-10-17" || ct.ClientID != adv.String() || ct.ContractorID != e.platform.String() ||
		ct.Type != domain.OrdIntermediaryContract || ct.AgentActingForPublisher == nil {
		t.Fatalf("contract request: %+v", ct)
	}
	// Ожидание зависимостей попытки не тратит.
	if e.count(`SELECT max(attempts) FROM job WHERE kind = 'sync_ord_object'`) != 0 {
		t.Fatal("dependency wait counted as an attempt")
	}
	e.settle(4)
	if c := e.contract(domain.ContractInitial, adv, e.platform); c.Sync.Status != domain.SyncSynced {
		t.Fatalf("contract not synced: %+v", c.Sync)
	}
	if len(e.logged("ord object confirmed")) == 0 || len(e.logged("ord dependency not ready")) == 0 {
		t.Fatal("confirmation or dependency wait not logged")
	}
}

func TestValidationErrorFailsWithoutRetry(t *testing.T) {
	e := newEnv(t)
	e.initialChain(e.adv)
	e.ord.FailNext(ord.OpContract, http.StatusUnprocessableEntity, "body.date: field required")
	e.settle(3)
	c := e.contract(domain.ContractInitial, e.adv, e.platform)
	if c.Sync.Status != domain.SyncError || c.Sync.Attempts != 1 || c.Sync.Error == "" {
		t.Fatalf("contract after 422: %+v", c.Sync)
	}
	if !e.alerts.has("ord_sync_failed:contract") {
		t.Fatal("no alert on ORD rejection")
	}
	calls := e.ord.Calls(ord.OpContract)
	e.settle(3)
	if e.ord.Calls(ord.OpContract) != calls {
		t.Fatal("rejected contract retried")
	}
}

func TestServerErrorsExhaustAttempts(t *testing.T) {
	e := newEnv(t)
	adv := markingtest.Organization(t, e.d.Pool, accountdomain.LegalEntity, "ООО Ошибка")
	// Отказы общие для платформы и рекламодателя: хватит, чтобы оба исчерпали попытки.
	for range 2 * app.SyncAttempts {
		e.ord.FailNext(ord.OpOrganization, http.StatusServiceUnavailable, "unavailable")
	}
	e.initialChain(adv)
	for range 2 * app.SyncAttempts {
		e.advance(time.Hour)
	}
	for _, id := range []uuid.UUID{e.platform, adv} {
		if o := e.org(id); o.Sync.Status != domain.SyncError || o.Sync.Attempts != app.SyncAttempts {
			t.Fatalf("organization %s after exhausted attempts: %+v", id, o.Sync)
		}
	}
	if !e.alerts.has("ord_sync_failed:organization") {
		t.Fatal("no alert after exhausted attempts")
	}
}

func TestOfferNotAccepted(t *testing.T) {
	e := newEnv(t)
	blogger := markingtest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "Блогер без оферты")
	e.publisherChain(blogger)
	e.settle(4)
	c := e.contract(domain.ContractPublisher, e.platform, blogger)
	if c.Sync.Status != domain.SyncError || c.Sync.Error != domain.ReasonOfferNotAccepted || c.Sync.Attempts != 0 {
		t.Fatalf("contract without offer: %+v", c.Sync)
	}
	if e.ord.Calls(ord.OpContract) != 0 {
		t.Fatal("contract without offer was sent")
	}
	if o := e.org(blogger); !o.IsRr {
		t.Fatalf("blogger organization must be isRr: %+v", o)
	}
}

func TestOrganizationChangeResends(t *testing.T) {
	e := newEnv(t)
	e.initialChain(e.adv)
	e.settle(5)
	if o := e.org(e.adv); o.Sync.Status != domain.SyncSynced {
		t.Fatalf("advertiser not synced: %+v", o.Sync)
	}
	ctx := context.Background()

	// Смена телефона в ОРД не уходит.
	if _, err := e.d.Pool.Exec(ctx, `UPDATE organization SET phone = '+79991112233' WHERE id = $1`, e.adv); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.OrganizationChanged(ctx, e.adv); err != nil {
		t.Fatal(err)
	}
	if o := e.org(e.adv); o.Sync.Status != domain.SyncSynced {
		t.Fatalf("phone change requeued organization: %+v", o.Sync)
	}

	// Новое наименование — повторная отправка с тем же id (R-15).
	if _, err := e.d.Pool.Exec(ctx, `UPDATE organization SET name = 'ООО Ромашка и партнёры' WHERE id = $1`, e.adv); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.OrganizationChanged(ctx, e.adv); err != nil {
		t.Fatal(err)
	}
	e.run()
	obj, _ := e.ord.Object(e.adv.String())
	var body ord.Organization
	if obj.Submits != 2 || obj.Decode(&body) != nil || body.Name != "ООО Ромашка и партнёры" {
		t.Fatalf("resubmit: %d %+v", obj.Submits, body)
	}
}

func TestUnknownOutcomeRetriesSameID(t *testing.T) {
	e := newEnv(t)
	adv := markingtest.Organization(t, e.d.Pool, accountdomain.LegalEntity, "ООО Таймаут")
	e.run()
	e.ord.TimeoutNext(ord.OpOrganization)
	e.initialChain(adv)
	e.run()
	e.advance(time.Minute)
	n := 0
	for id, o := range e.ord.Objects() {
		if o.Op == ord.OpOrganization && (id == adv.String() || id == e.platform.String()) {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d organizations in ORD, want 2 (platform and advertiser)", n)
	}
	for _, id := range []uuid.UUID{e.platform, adv} {
		if o := e.org(id); o.Sync.Status != domain.SyncSent && o.Sync.Status != domain.SyncSynced {
			t.Fatalf("organization %s after timeout retry: %+v", id, o.Sync)
		}
	}
}
