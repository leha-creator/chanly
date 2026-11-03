package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func TestInviteAndActivate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	v, err := e.svc.Status(ctx, org)
	if err != nil || !v.Required || v.Status != domain.ContractorActive || v.OnboardingURL == "" {
		t.Fatalf("status %+v: %v", v, err)
	}
	if ok, err := e.svc.CanAccept(ctx, org); err != nil || !ok {
		t.Fatalf("can accept %v: %v", ok, err)
	}
	if e.notices.count(domain.NoticeRocketworkActive) != 1 {
		t.Fatal("no rocketwork.active notice")
	}
	if e.jobs(app.EnsureContractorKey(org)) != 1 {
		t.Fatal("ensure_contractor not enqueued")
	}
	// Повтор приглашения — тот же исполнитель и без второго вызова сервиса.
	if _, err := e.svc.Invite(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := e.rw.CountCalls("EnsureContractor"); n != 1 {
		t.Fatalf("ensure calls %d", n)
	}
}

func TestInviteRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	legal := e.blogger(accountdomain.LegalEntity)
	_, err := e.svc.Invite(ctx, legal)
	mustErr(t, err, domain.ErrContractorNotRequired)
	if v, err := e.svc.Status(ctx, legal); err != nil || v.Required {
		t.Fatalf("legal status %+v: %v", v, err)
	}
	if ok, err := e.svc.CanAccept(ctx, legal); err != nil || !ok {
		t.Fatalf("legal can accept %v: %v", ok, err)
	}
	// Исполнителя нет — принимать заявку нельзя.
	bare := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "Без исполнителя")
	if ok, err := e.svc.CanAccept(ctx, bare); err != nil || ok {
		t.Fatalf("no contractor can accept %v: %v", ok, err)
	}
	if v, err := e.svc.Status(ctx, bare); err != nil || v.Status != domain.ContractorNone {
		t.Fatalf("no contractor status %+v: %v", v, err)
	}
}

func TestContractorBlockedByWebhook(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SoleProprietor)
	ext := e.contractor(org).ExternalID
	if err := e.rw.BlockContractor(ext, rocketwork.FailureTaxStatusLost); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ApplyContractor(ctx, ext); err != nil {
		t.Fatal(err)
	}
	c := e.contractor(org)
	if c.Status != domain.ContractorBlocked || c.BlockedReason != string(domain.FailureTaxStatusLost) {
		t.Fatalf("contractor %s %s", c.Status, c.BlockedReason)
	}
	if ok, _ := e.svc.CanAccept(ctx, org); ok {
		t.Fatal("blocked contractor accepted")
	}
	if e.notices.count(domain.NoticeRocketworkBlocked) != 1 {
		t.Fatal("no rocketwork.blocked notice")
	}
	// Повтор события — без второго уведомления.
	if err := e.svc.ApplyContractor(ctx, ext); err != nil {
		t.Fatal(err)
	}
	if e.notices.count(domain.NoticeRocketworkBlocked) != 1 {
		t.Fatal("duplicate notice")
	}
}

func TestPayoutSucceeds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	if err := e.rw.SetDeposit(1_000_000); err != nil {
		t.Fatal(err)
	}
	pid, id := e.accepted(org, 50_000)
	p := e.payout(id)
	if p.Status != domain.StatusCreated || p.Method != domain.MethodRocketwork || p.Attempt != 1 ||
		p.IdempotencyKey != domain.IdempotencyKey(id) {
		t.Fatalf("created %+v", p)
	}
	if e.jobs(app.SendKey(id)) != 1 {
		t.Fatal("send_payout not enqueued")
	}
	// Повтор Create по тому же размещению — та же выплата.
	if err := e.tx.InTx(ctx, func(ctx context.Context) error {
		again, err := e.svc.Create(ctx, app.PayoutRequest{PlacementID: pid, BloggerOrgID: org, Amount: 50_000})
		if err == nil && again != id {
			t.Errorf("second create %s", again)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatalf("send: %v", err)
	}
	p = e.payout(id)
	if p.Status != domain.StatusSent || p.ExternalID == "" || p.SentAt == nil {
		t.Fatalf("sent %+v", p)
	}
	if e.jobs(app.PollPayoutKey(id)) != 1 || e.notices.count(domain.NoticePayoutSent) != 1 {
		t.Fatal("poll not enqueued or no notice")
	}
	// Повтор отправки — без второго CreatePayout.
	if err := e.svc.Send(ctx, id, 2); err != nil || e.rw.CountCalls("CreatePayout") != 1 {
		t.Fatalf("resend: %v, calls %d", err, e.rw.CountCalls("CreatePayout"))
	}

	// В работе: опрос откладывается.
	if err := e.svc.PollPayout(ctx, id); !isPostponed(err) {
		t.Fatalf("poll in progress: %v", err)
	}
	if err := e.rw.CompletePayout(p.ExternalID, -1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PollPayout(ctx, id); err != nil {
		t.Fatalf("poll: %v", err)
	}
	p = e.payout(id)
	if p.Status != domain.StatusSucceeded || p.ProviderFee != 1500 || p.ReceiptURL == "" || p.CompletedAt == nil {
		t.Fatalf("succeeded %+v", p)
	}
	if e.payable(org) != 0 || e.placements.paidCount() != 1 {
		t.Fatalf("payable %d, paid %d", e.payable(org), e.placements.paidCount())
	}
	if n := e.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'payout' AND payout_id = $1`, id); n != 1 {
		t.Fatalf("payout transactions %d", n)
	}
	// Повтор события после успеха — no-op.
	if err := e.svc.ApplyPayout(ctx, p.ExternalID); err != nil || e.placements.paidCount() != 1 {
		t.Fatalf("replay: %v", err)
	}
	if e.notices.count(domain.NoticePayoutSucceeded) != 1 {
		t.Fatal("succeeded notice")
	}
	sum, err := e.svc.Summary(ctx, org)
	if err != nil || sum.Received != 50_000 || sum.InProgress != 0 || sum.Count != 1 {
		t.Fatalf("summary %+v: %v", sum, err)
	}
	page, err := e.svc.List(ctx, org, "", 10)
	if err != nil || len(page.Items) != 1 || page.Total != 1 || page.Items[0].Brief.OrderNumber != 7 {
		t.Fatalf("list %+v: %v", page, err)
	}
}

func TestSendDepositShort(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_, id := e.accepted(org, 50_000)
	err := e.svc.Send(ctx, id, 1)
	if d, ok := job.PostponedFor(err); !ok || d != time.Hour {
		t.Fatalf("send with empty deposit: %v", err)
	}
	if p := e.payout(id); p.Status != domain.StatusCreated {
		t.Fatalf("status %s", p.Status)
	}
	if !e.alerts.isOpen(app.AlertDepositInsufficient, "payout:"+id.String()) {
		t.Fatal("no deposit alert")
	}
	if e.rw.CountCalls("CreatePayout") != 0 {
		t.Fatal("payout created without deposit")
	}
	// Пополнили — отправка проходит, оповещение закрыто.
	if err := e.rw.SetDeposit(100_000); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Send(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	if e.alerts.isOpen(app.AlertDepositInsufficient, "payout:"+id.String()) {
		t.Fatal("deposit alert not resolved")
	}
}

func TestSendUnknownOutcome(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(100_000)
	_, id := e.accepted(org, 50_000)
	e.rw.TimeoutNext()
	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatalf("send after timeout: %v", err)
	}
	if p := e.payout(id); p.Status != domain.StatusSent {
		t.Fatalf("status %s", p.Status)
	}
	if n := len(e.rw.Payments()); n != 1 {
		t.Fatalf("payments %d", n)
	}
	// Сервис недоступен и выплаты нет — повтор задания с тем же ключом.
	_, id2 := e.accepted(org, 30_000)
	e.rw.FailNext("CreatePayout", rocketwork.ErrUnavailable)
	if err := e.svc.Send(ctx, id2, 1); err == nil || isPostponed(err) {
		t.Fatalf("send unavailable: %v", err)
	}
	if p := e.payout(id2); p.Status != domain.StatusCreated {
		t.Fatalf("status %s", p.Status)
	}
}

func TestSendContractorLostStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(100_000)
	_, id := e.accepted(org, 50_000)
	_ = e.rw.BlockContractor(e.contractor(org).ExternalID, rocketwork.FailureTaxStatusLost)
	e.clk.Advance(2 * time.Hour) // статус устарел — перед выплатой GetContractor
	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	p := e.payout(id)
	if p.Status != domain.StatusFailed || p.FailureCode != domain.FailureTaxStatusLost {
		t.Fatalf("payout %s %s", p.Status, p.FailureCode)
	}
	if !e.alerts.isOpen(app.AlertPayoutFailed, "payout:"+id.String()) || e.notices.count(domain.NoticePayoutFailed) != 1 {
		t.Fatal("no failure alert or notice")
	}
	if e.rw.CountCalls("CreatePayout") != 0 {
		t.Fatal("payout sent to inactive contractor")
	}
}

func TestPayoutFailedAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(1_000_000)
	_, id := e.accepted(org, 50_000)
	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.rw.FailPayout(e.payout(id).ExternalID, "bank_rejected", "счёт закрыт"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PollPayout(ctx, id); err != nil {
		t.Fatal(err)
	}
	if p := e.payout(id); p.Status != domain.StatusFailed || p.FailureCode != domain.FailureProviderError {
		t.Fatalf("failed %s %s", p.Status, p.FailureCode)
	}
	if e.payable(org) != 50_000 {
		t.Fatal("payable changed on failure")
	}
	_, err := e.svc.Retry(ctx, id, e.admin, "")
	mustErr(t, err, domain.ErrInvalidPayout)
	next, err := e.svc.Retry(ctx, id, e.admin, "реквизиты исправлены")
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt != 2 || next.IdempotencyKey == e.payout(id).IdempotencyKey || next.Status != domain.StatusCreated {
		t.Fatalf("next %+v", next)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND object_id = $2`, app.ActionRetry, next.ID.String()) != 1 {
		t.Fatal("retry not audited")
	}
	// Повтор старой попытки и живой попытки отклоняется.
	_, err = e.svc.Retry(ctx, id, e.admin, "ещё раз")
	mustErr(t, err, domain.ErrPayoutNotRetryable)
	_, err = e.svc.Retry(ctx, next.ID, e.admin, "ещё раз")
	if err == nil {
		t.Fatal("retry of a live payout")
	}
	if err := e.svc.Send(ctx, next.ID, 1); err != nil {
		t.Fatal(err)
	}
	_ = e.rw.CompletePayout(e.payout(next.ID).ExternalID, 0)
	if err := e.svc.PollPayout(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	if p := e.payout(next.ID); p.Status != domain.StatusSucceeded || e.payable(org) != 0 {
		t.Fatalf("second attempt %s, payable %d", p.Status, e.payable(org))
	}
}

func TestPayoutPlacementStateMismatch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(1_000_000)
	_, id := e.accepted(org, 50_000)
	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	_ = e.rw.CompletePayout(e.payout(id).ExternalID, 0)
	e.placements.failPaid = domain.ErrPlacementState
	err := e.svc.PollPayout(ctx, id)
	if !job.IsPermanent(err) {
		t.Fatalf("poll with wrong placement state: %v", err)
	}
	if p := e.payout(id); p.Status != domain.StatusSent {
		t.Fatalf("status %s", p.Status)
	}
	if e.payable(org) != 50_000 {
		t.Fatal("ledger posted despite rollback")
	}
	if !e.alerts.isOpen(app.AlertStateMismatch, "payout:"+id.String()) {
		t.Fatal("no state mismatch alert")
	}
}

func TestLegalEntityMarkPaid(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.LegalEntity)
	_, id := e.accepted(org, 80_000)
	p := e.payout(id)
	if p.Method != domain.MethodBankTransfer || e.jobs(app.SendKey(id)) != 0 {
		t.Fatalf("legal payout %+v", p)
	}
	_, err := e.svc.MarkLegalPaid(ctx, id, e.admin, uuid.Nil, "", "оплачено")
	mustErr(t, err, domain.ErrInvalidPayout)
	if p, err = e.svc.MarkLegalPaid(ctx, id, e.admin, uuid.Nil, "п/п 15", "оплачено"); err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusSucceeded || p.BankReference != "п/п 15" || e.payable(org) != 0 || e.placements.paidCount() != 1 {
		t.Fatalf("legal paid %+v", p)
	}
	// Повтор — без второй проводки.
	if _, err := e.svc.MarkLegalPaid(ctx, id, e.admin, uuid.Nil, "п/п 15", "оплачено"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM ledger_transaction WHERE payout_id = $1`, id); n != 1 {
		t.Fatalf("transactions %d", n)
	}
	// Выплату через Рокет Ворк вручную не отмечают.
	rw := e.blogger(accountdomain.SelfEmployed)
	_, rid := e.accepted(rw, 10_000)
	_, err = e.svc.MarkLegalPaid(ctx, rid, e.admin, uuid.Nil, "п/п 16", "оплачено")
	mustErr(t, err, domain.ErrInvalidPayoutState)
}

func TestCreateWarnsNearNPDLimit(t *testing.T) {
	e := newEnv(t)
	org := e.blogger(accountdomain.SelfEmployed)
	e.accepted(org, 95_000) // лимит теста 100 000 коп.
	if !e.alerts.isOpen(app.AlertNPDLimit, "organization:"+org.String()) {
		t.Fatal("no npd alert")
	}
}

func TestCheckDeposit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	e.accepted(org, 50_000)
	r, err := e.svc.CheckDeposit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Payable != 50_000 || r.Shortage() != 50_000 || !e.alerts.isOpen(app.AlertDepositLow, "") {
		t.Fatalf("report %+v", r)
	}
	_ = e.rw.SetDeposit(60_000)
	if r, err = e.svc.CheckDeposit(ctx); err != nil || r.Shortage() != 0 {
		t.Fatalf("report %+v: %v", r, err)
	}
	if e.alerts.isOpen(app.AlertDepositLow, "") {
		t.Fatal("low deposit alert not resolved")
	}
	// Журнал (0) и сервис (60 000) расходятся.
	if !r.Mismatch() || !e.alerts.isOpen(app.AlertDepositMismatch, "") {
		t.Fatal("no mismatch")
	}
}

func TestProviderDisabled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	disabled := app.New(app.Deps{Store: e.svc.Store, Provider: e.svc.Provider, Ledger: e.svc.Ledger,
		Placements: e.svc.Placements, Organizations: e.svc.Organizations, Tx: e.tx, Jobs: e.svc.Jobs,
		Audit: e.svc.Audit, Clock: e.clk, Log: e.svc.Log})
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "Блогер")
	_, err := disabled.Invite(ctx, org)
	mustErr(t, err, domain.ErrProviderDisabled)
	e.rw.FailNext("GetDepositBalance", rocketwork.ErrDisabled)
	if _, err := e.svc.CheckDeposit(ctx); !isPostponed(err) {
		t.Fatalf("check deposit disabled: %v", err)
	}
}

func TestDepositExcludesLegalAndTolerance(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.accepted(e.blogger(accountdomain.LegalEntity), 70_000)
	e.accepted(e.blogger(accountdomain.SoleProprietor), 20_000)
	_ = e.rw.SetDeposit(100)
	r, err := e.svc.CheckDeposit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Payable != 20_000 || r.Need != 20_000 {
		t.Fatalf("legal entity counted: %+v", r)
	}
	// Допуск 1,00 ₽: журнал 0, сервис 1,00 ₽ — не расхождение; 1,01 ₽ — расхождение.
	if r.Mismatch() {
		t.Fatalf("100 kopecks is within tolerance: %+v", r)
	}
	_ = e.rw.SetDeposit(101)
	if r, _ = e.svc.CheckDeposit(ctx); !r.Mismatch() {
		t.Fatalf("101 kopecks over tolerance: %+v", r)
	}
}

func TestStuckChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	pid, id := e.accepted(org, 50_000)
	if _, err := e.d.Pool.Exec(ctx, `UPDATE placement SET status = 'accepted', status_changed_at = $2, performance_share = 10000,
		payout_amount = blogger_amount, fee_charged = fee_amount, refund_amount = 0 WHERE id = $1`, pid, start); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func(context.Context, time.Time) ([]string, error){}
	for _, c := range e.svc.StuckChecks() {
		checks[c.Name] = c.Find
	}
	find := func(name string, before time.Time) int {
		t.Helper()
		keys, err := checks[name](ctx, before)
		if err != nil {
			t.Fatal(err)
		}
		return len(keys)
	}
	for _, name := range []string{app.StuckAcceptedUnpaid, app.StuckCreatedNotSent} {
		if find(name, start) != 0 || find(name, start.Add(time.Second)) != 1 {
			t.Fatalf("%s boundary", name)
		}
	}
	if find(app.StuckSentNoOutcome, start.Add(time.Second)) != 0 {
		t.Fatal("created payout counted as sent")
	}
	_ = e.rw.SetDeposit(100_000)
	if err := e.svc.Send(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if find(app.StuckSentNoOutcome, start.Add(time.Second)) != 1 || find(app.StuckCreatedNotSent, start.Add(time.Second)) != 0 {
		t.Fatal("sent payout not found")
	}
}

// Ревью: прошлый запуск создал выплату с неизвестным исходом — следующий находит её по ключу
// до проверок исполнителя и депозита и не помечает failed (иначе повтор заплатил бы дважды).
func TestSendFindsPayoutBeforeChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(100_000)
	_, id := e.accepted(org, 50_000)
	p := e.payout(id)
	if _, err := e.rw.CreatePayout(ctx, rocketwork.PaymentRequest{IdempotencyKey: p.IdempotencyKey, ExternalID: id.String(),
		ContractorID: e.contractor(org).ExternalID, AmountKopecks: 50_000}); err != nil {
		t.Fatal(err)
	}
	_ = e.rw.BlockContractor(e.contractor(org).ExternalID, rocketwork.FailureTaxStatusLost)
	_ = e.rw.SetDeposit(0)
	if err := e.svc.Send(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	if p := e.payout(id); p.Status != domain.StatusSent || p.ExternalID == "" {
		t.Fatalf("payout %s %s", p.Status, p.FailureCode)
	}
	if n := e.rw.CountCalls("CreatePayout"); n != 1 {
		t.Fatalf("create calls %d", n)
	}
}

// Ревью: исчерпание send_payout — найденная в сервисе выплата становится sent, ненайденная —
// failed provider_error, при неизвестном исходе failed не ставится.
func TestSendExhaustedChecksProvider(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	_ = e.rw.SetDeposit(1_000_000)
	_, found := e.accepted(org, 50_000)
	p := e.payout(found)
	if _, err := e.rw.CreatePayout(ctx, rocketwork.PaymentRequest{IdempotencyKey: p.IdempotencyKey, ExternalID: found.String(),
		ContractorID: e.contractor(org).ExternalID, AmountKopecks: 50_000}); err != nil {
		t.Fatal(err)
	}
	e.svc.SendExhausted(ctx, found, errors.New("boom"))
	if p := e.payout(found); p.Status != domain.StatusSent {
		t.Fatalf("found: %s", p.Status)
	}

	org2 := e.blogger(accountdomain.SelfEmployed)
	_, unknown := e.accepted(org2, 40_000)
	e.rw.FailNext("FindPayout", rocketwork.ErrUnavailable)
	e.svc.SendExhausted(ctx, unknown, errors.New("boom"))
	if p := e.payout(unknown); p.Status != domain.StatusCreated {
		t.Fatalf("unknown: %s", p.Status)
	}
	if !e.alerts.isOpen(app.AlertStateMismatch, "payout:"+unknown.String()) {
		t.Fatal("mismatch alert for unknown outcome")
	}
	e.svc.SendExhausted(ctx, unknown, errors.New("boom"))
	if p := e.payout(unknown); p.Status != domain.StatusFailed || p.FailureCode != domain.FailureProviderError {
		t.Fatalf("not found: %s %s", p.Status, p.FailureCode)
	}
}
