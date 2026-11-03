package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// acceptedPayout — размещение в accepted с начислением blogger_payable и выплатой, созданной как
// при переходе в accepted. legal — получатель юрлицо (перевод вручную), иначе самозанятый с
// исполнителем active.
func (a *testApp) acceptedPayout(legal bool, amount money.Kopecks) (placementID, bloggerID, payoutID uuid.UUID) {
	a.t.Helper()
	if legal {
		bloggerID = ledgertest.Organization(a.t, a.db.Pool, accountdomain.LegalEntity, "ООО Блогер")
	} else {
		bloggerID = ledgertest.Organization(a.t, a.db.Pool, accountdomain.SelfEmployed, "Блогер")
		a.activeContractor(bloggerID)
	}
	return a.acceptedPayoutFor(bloggerID, amount)
}

// acceptedPayoutFor — то же для готовой организации блогера (исполнитель уже active или юрлицо).
func (a *testApp) acceptedPayoutFor(bloggerID uuid.UUID, amount money.Kopecks) (placementID, _, payoutID uuid.UUID) {
	a.t.Helper()
	ctx := context.Background()
	adv := ledgertest.Organization(a.t, a.db.Pool, accountdomain.LegalEntity, "Рекламодатель")
	p := ledgertest.NewPlacement(a.t, a.db.Pool, adv, amount)
	if _, err := a.db.Pool.Exec(ctx, `UPDATE post_version SET content_doc = '{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"текст"}]}]}'
		WHERE id = $1`, p.VersionID); err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.db.Pool.Exec(ctx, `UPDATE placement SET blogger_organization_id = $2, status = 'accepted',
		performance_share = 10000, payout_amount = blogger_amount, fee_charged = fee_amount, refund_amount = 0,
		lock_version = lock_version + 1 WHERE id = $1`, p.ID, bloggerID); err != nil {
		a.t.Fatal(err)
	}
	admin := ledgertest.User(a.t, a.db.Pool)
	err := a.mods.txRunner.InTx(ctx, func(ctx context.Context) error {
		if _, err := a.mods.ledger.Post(ctx, ledgerdomain.NewManual("payable:"+p.ID.String(), []ledgerdomain.Posting{
			{Debit: ledgerdomain.Bank(), Credit: ledgerdomain.BloggerPayable(bloggerID), Amount: amount}}, admin, "начисление")); err != nil {
			return err
		}
		var err error
		payoutID, err = a.mods.payout.Create(ctx, payoutapp.PayoutRequest{PlacementID: p.ID, BloggerOrgID: bloggerID, Amount: amount})
		return err
	})
	if err != nil {
		a.t.Fatalf("accepted payout: %v", err)
	}
	return p.ID, bloggerID, payoutID
}

// Этап выплат, R-3, R-9, R-12: maxim admin payout и maxim admin money deposit-topup.
func TestAdminPayout(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAdmin(ctx, cfg, a.mods.in.log, args, &out)
		return out.String(), err
	}
	for email, role := range map[string]accountdomain.AdminRole{"senior1@example.ru": accountdomain.AdminSenior,
		"senior2@example.ru": accountdomain.AdminSenior, "moderator@example.ru": accountdomain.AdminModerator} {
		if _, err := a.mods.account.SetAdminRole(ctx, email, &role); err != nil {
			t.Fatal(err)
		}
	}

	// Юрлицо: перевод вручную, P-16 и проводка blogger_payable → bank.
	pid, legal, id := a.acceptedPayout(true, 80_000)
	out, err := run("payout", "list", "--status", "created")
	if err != nil || !strings.Contains(out, id.String()) || !strings.Contains(out, "bank_transfer") {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, err := run("payout", "mark-paid", "--payout", id.String(), "--bank-ref", "п/п 7", "--reason", "оплачено",
		"--email", "moderator@example.ru"); !errors.Is(err, errNotSenior) {
		t.Fatalf("mark-paid by moderator: %v", err)
	}
	if _, err := run("payout", "mark-paid", "--payout", id.String(), "--reason", "оплачено", "--email", "senior1@example.ru"); !errors.Is(err, errUsage) {
		t.Fatalf("mark-paid without bank ref: %v", err)
	}
	out, err = run("payout", "mark-paid", "--payout", id.String(), "--bank-ref", "п/п 7", "--reason", "оплачено",
		"--email", "senior1@example.ru")
	if err != nil || !strings.Contains(out, "succeeded") {
		t.Fatalf("mark-paid: %q %v", out, err)
	}
	if status, _ := a.placementState(pid.String()); status != "paid" {
		t.Fatalf("placement %s", status)
	}
	if n := a.count(`SELECT count(*) FROM ledger_account WHERE kind = 'blogger_payable' AND organization_id = $1 AND balance = 0`, legal); n != 1 {
		t.Fatal("payable not cleared")
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND object_id = $2`, payoutapp.ActionMarkPaid, id.String()); n != 1 {
		t.Fatalf("audit %d", n)
	}

	// Самозанятый: отказ сервиса и повтор администратором.
	rw := a.rocketWork()
	_ = rw.SetDeposit(1_000_000)
	_, _, rid := a.acceptedPayout(false, 30_000)
	if _, err := run("payout", "retry", "--payout", rid.String(), "--reason", "ещё раз", "--email", "senior1@example.ru"); !errors.Is(err, payoutdomain.ErrPayoutNotRetryable) {
		t.Fatalf("retry of created payout: %v", err)
	}
	if err := a.mods.payout.Send(ctx, rid, 1); err != nil {
		t.Fatal(err)
	}
	p, _ := a.mods.payout.Payout(ctx, rid)
	_ = rw.FailPayout(p.ExternalID, "rejected", "отказ банка")
	if err := a.mods.payout.PollPayout(ctx, rid); err != nil {
		t.Fatal(err)
	}
	out, err = run("payout", "retry", "--payout", rid.String(), "--reason", "реквизиты исправлены", "--email", "senior1@example.ru")
	if err != nil || !strings.Contains(out, "attempt 2") {
		t.Fatalf("retry: %q %v", out, err)
	}
	if n := a.count(`SELECT count(*) FROM job WHERE kind = 'send_payout' AND dedupe_key <> $1`, payoutapp.SendKey(rid)); n != 1 {
		t.Fatalf("send jobs %d", n)
	}
	if _, err := run("payout", "mark-paid", "--payout", rid.String(), "--bank-ref", "п/п 8", "--reason", "оплачено",
		"--email", "senior1@example.ru"); !errors.Is(err, payoutdomain.ErrInvalidPayoutState) {
		t.Fatalf("mark-paid of rocketwork payout: %v", err)
	}

	// Пополнение депозита: повтор с тем же ключом — без второй проводки.
	topup := []string{"money", "deposit-topup", "--amount", "5000.00", "--reason", "пополнение", "--email", "senior1@example.ru", "--key", "rw-1"}
	if out, err = run(topup...); err != nil || !strings.Contains(out, "replayed false") {
		t.Fatalf("topup: %q %v", out, err)
	}
	if out, err = run(topup...); err != nil || !strings.Contains(out, "replayed true") {
		t.Fatalf("topup replay: %q %v", out, err)
	}
	totals, err := a.mods.ledger.Totals(ctx)
	if err != nil || totals[ledgerdomain.AccountRocketworkDeposit] != 500_000 {
		t.Fatalf("deposit %d: %v", totals[ledgerdomain.AccountRocketworkDeposit], err)
	}
	if out, err = run("money", "reconcile"); err != nil || !strings.Contains(out, "rocketwork deposit: ledger 5000.00 RUB") {
		t.Fatalf("reconcile: %q %v", out, err)
	}
	if _, err := run("payout", "unknown"); !errors.Is(err, errUsage) {
		t.Fatalf("unknown: %v", err)
	}
}
