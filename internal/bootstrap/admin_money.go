package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// modeAdminMoney — инфраструктура служебных команд денег: им нужно хранилище (файл выписки).
const modeAdminMoney = "admin-money"

// errNotSenior — у пользователя команды нет роли senior (R-14).
var errNotSenior = errors.New("user is not a senior admin")

// moneyFlags — флаги команд maxim admin money.
type moneyFlags struct {
	fs                                 *flag.FlagSet
	file, email, approver, reason, key *string
	id, org, invoice, number, status   *string
	debit, credit, amount, kind, since *string
	period                             *string
	limit                              *int
}

func newMoneyFlags(name string, out io.Writer) *moneyFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return &moneyFlags{
		fs: fs, file: fs.String("file", "", "statement file"), email: fs.String("email", "", "senior admin email"),
		approver: fs.String("approver-email", "", "second senior admin email"), reason: fs.String("reason", "", "reason"),
		key: fs.String("key", "", "idempotency key of the command"), id: fs.String("id", "", "bank transaction id"),
		org: fs.String("org", "", "organization id or INN"), invoice: fs.String("invoice", "", "invoice number MX-…"),
		number: fs.String("number", "", "invoice number MX-…"), status: fs.String("status", "", "matched | unmatched | ignored"),
		debit: fs.String("debit", "", "kind[:scope]"), credit: fs.String("credit", "", "kind[:scope]"),
		amount: fs.String("amount", "", "rubles.kopecks"), kind: fs.String("kind", "", "ledger transaction kind"),
		since: fs.String("since", "", "YYYY-MM-DD"), limit: fs.Int("limit", 50, "max rows"),
		period: fs.String("period", "", "YYYY-MM"),
	}
}

// runAdminMoney — деньги до API администратора (S-19, S-30, R-11, R-13, R-14, 06 §2.4):
//
//	maxim admin money statement import --file <путь> --email <адм>
//	maxim admin money bank-tx list [--status unmatched] [--limit N]
//	maxim admin money bank-tx match --id <id> --org <id|ИНН> [--invoice MX-…] --reason … --email …
//	maxim admin money bank-tx ignore --id <id> --reason … --email …
//	maxim admin money invoice cancel --number MX-… --reason … --email …
//	maxim admin money ledger manual --debit <вид[:разрез]> --credit <вид[:разрез]> --amount <руб.коп> --reason … --email … [--approver-email …] [--key …]
//	maxim admin money ledger list [--org …] [--kind …] [--since YYYY-MM-DD] [--limit N]
//	maxim admin money balance-return --org … --amount … --reason … --email … [--key …]
//	maxim admin money deposit-topup --amount … --reason … --email … [--approver-email …] [--key …] — пополнение депозита Рокет Ворк
//	maxim admin money reconcile
//	maxim admin money closing-acts --period YYYY-MM --email … — закрывающие документы за месяц вручную (R-16)
//
// Изменяющие команды требуют администратора senior и основание; запись в audit_log с актором admin.
func runAdminMoney(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminMoney"
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	if cmd == "statement" || cmd == "bank-tx" || cmd == "invoice" || cmd == "ledger" {
		if len(rest) == 0 {
			return errUsage
		}
		cmd, rest = cmd+" "+rest[0], rest[1:]
	}
	f := newMoneyFlags("admin money "+cmd, out)
	if err := f.fs.Parse(rest); err != nil || f.fs.NArg() != 0 {
		return errUsage
	}
	need := func(vals ...*string) bool {
		for _, v := range vals {
			if strings.TrimSpace(*v) == "" {
				return false
			}
		}
		return true
	}
	ok := true
	switch cmd {
	case "statement import":
		ok = need(f.file, f.email)
	case "bank-tx list", "ledger list", "reconcile":
		ok = *f.limit > 0
	case "bank-tx match":
		ok = need(f.id, f.org, f.reason, f.email)
	case "bank-tx ignore":
		ok = need(f.id, f.reason, f.email)
	case "invoice cancel":
		ok = need(f.number, f.reason, f.email)
	case "ledger manual":
		ok = need(f.debit, f.credit, f.amount, f.reason, f.email)
	case "balance-return":
		ok = need(f.org, f.amount, f.reason, f.email)
	case "deposit-topup":
		ok = need(f.amount, f.reason, f.email)
	case "closing-acts":
		ok = need(f.period, f.email)
	default:
		log.ErrorContext(ctx, "unknown admin money command", logx.KeyOp, op, "command", cmd)
		return errUsage
	}
	if !ok {
		return errUsage
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, modeAdminMoney, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	c := &moneyCmd{m: m, users: accountpg.New(pool, log).Users(), log: log, out: out, f: f}
	return c.run(ctx, cmd)
}

type moneyCmd struct {
	m     *modules
	users *accountpg.Users
	log   *slog.Logger
	out   io.Writer
	f     *moneyFlags
}

func (c *moneyCmd) run(ctx context.Context, cmd string) error {
	switch cmd {
	case "bank-tx list":
		return c.listBankTx(ctx)
	case "ledger list":
		return c.listLedger(ctx)
	case "reconcile":
		return c.reconcile(ctx)
	}
	admin, err := c.senior(ctx, *c.f.email)
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &admin})
	switch cmd {
	case "statement import":
		return c.importStatement(ctx, admin)
	case "bank-tx match":
		return c.matchBankTx(ctx, admin)
	case "bank-tx ignore":
		id, err := uuid.Parse(*c.f.id)
		if err != nil {
			return errUsage
		}
		bt, err := c.m.billing.Ignore(ctx, id, admin, *c.f.reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "bank transaction %s ignored\n", bt.ID)
		return nil
	case "invoice cancel":
		inv, err := c.m.billing.InvoiceByNumber(ctx, *c.f.number)
		if err != nil {
			return err
		}
		if _, err := c.m.billing.CancelInvoice(ctx, inv.ID, admin, *c.f.reason); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "invoice %s cancelled\n", inv.Number)
		return nil
	case "ledger manual":
		return c.manual(ctx, admin)
	case "balance-return":
		return c.balanceReturn(ctx, admin)
	case "deposit-topup":
		return c.depositTopup(ctx, admin)
	case "closing-acts":
		return c.closingActs(ctx)
	}
	return errUsage
}

// senior — администратор с ролью senior по почте (R-14).
func (c *moneyCmd) senior(ctx context.Context, email string) (uuid.UUID, error) {
	user, err := c.users.GetByEmail(ctx, accountdomain.NormalizeEmail(email))
	if errors.Is(err, accountdomain.ErrNotFound) || (err == nil && (user.AdminRole == nil || *user.AdminRole != accountdomain.AdminSenior)) {
		c.log.WarnContext(ctx, "admin money command refused", logx.KeyOp, "Bootstrap.AdminMoney", "reason", "not_senior")
		return uuid.Nil, errNotSenior
	}
	if err != nil {
		return uuid.Nil, err
	}
	return user.ID, nil
}

// orgID — организация по идентификатору или ИНН.
func (c *moneyCmd) orgID(ctx context.Context, s string) (uuid.UUID, error) {
	if id, err := uuid.Parse(s); err == nil {
		return id, nil
	}
	org, err := c.m.billing.OrganizationByINN(ctx, s)
	if err != nil {
		return uuid.Nil, err
	}
	return org.ID, nil
}

// amount — сумма «рубли.копейки» без float (money.Parse); не положительная — ошибка аргументов.
func (c *moneyCmd) amount() (money.Kopecks, error) {
	v, err := money.Parse(*c.f.amount)
	if err != nil || v <= 0 {
		fmt.Fprintln(c.out, "amount must be positive rubles.kopecks, e.g. 1500.00")
		return 0, errUsage
	}
	return v, nil
}

// key — ключ идемпотентности команды: заданный или новый, печатается для повтора (R-3).
func (c *moneyCmd) key() string {
	if *c.f.key != "" {
		return *c.f.key
	}
	k := ids.New().String()
	fmt.Fprintf(c.out, "key %s (repeat the command with --key %s to retry safely)\n", k, k)
	return k
}

func (c *moneyCmd) importStatement(ctx context.Context, admin uuid.UUID) error {
	file, err := os.Open(*c.f.file)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(c.m.cfg.StatementMaxBytes())+1))
	if err != nil {
		return err
	}
	res, err := c.m.billing.ImportStatement(ctx, admin, data)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "statement %s: incoming %d, matched %d, unmatched %d, duplicates %d, credited %s\n",
		res.StatementID, res.Incoming, res.Matched, res.Unmatched, res.Duplicates, rub(res.Credited))
	return nil
}

func (c *moneyCmd) matchBankTx(ctx context.Context, admin uuid.UUID) error {
	id, err := uuid.Parse(*c.f.id)
	if err != nil {
		return errUsage
	}
	org, err := c.orgID(ctx, *c.f.org)
	if err != nil {
		return err
	}
	invoiceID := uuid.Nil
	if *c.f.invoice != "" {
		inv, err := c.m.billing.InvoiceByNumber(ctx, *c.f.invoice)
		if err != nil {
			return err
		}
		invoiceID = inv.ID
	}
	bt, err := c.m.billing.MatchManually(ctx, id, org, invoiceID, admin, *c.f.reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "bank transaction %s matched to organization %s, credited %s\n", bt.ID, org, rub(bt.Amount))
	return nil
}

func (c *moneyCmd) manual(ctx context.Context, admin uuid.UUID) error {
	amount, err := c.amount()
	if err != nil {
		return err
	}
	debit, err := c.account(ctx, *c.f.debit)
	if err != nil {
		return err
	}
	credit, err := c.account(ctx, *c.f.credit)
	if err != nil {
		return err
	}
	approver := uuid.Nil
	if *c.f.approver != "" {
		if approver, err = c.senior(ctx, *c.f.approver); err != nil {
			return err
		}
	}
	res, err := c.m.ledger.Manual(ctx, ledgerapp.ManualRequest{Key: c.key(), Debit: debit, Credit: credit, Amount: amount,
		AdminID: admin, ApproverID: approver, Reason: *c.f.reason})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "manual transaction %s posted (replayed %t)\n", res.ID, res.Replayed)
	return nil
}

func (c *moneyCmd) balanceReturn(ctx context.Context, admin uuid.UUID) error {
	amount, err := c.amount()
	if err != nil {
		return err
	}
	org, err := c.orgID(ctx, *c.f.org)
	if err != nil {
		return err
	}
	res, err := c.m.ledger.BalanceReturn(ctx, c.key(), org, amount, admin, *c.f.reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "balance return %s posted (replayed %t)\n", res.ID, res.Replayed)
	return nil
}

// depositTopup — перевод с расчётного счёта на депозит Рокет Ворк (06 §2.3): bank → rocketwork_deposit.
func (c *moneyCmd) depositTopup(ctx context.Context, admin uuid.UUID) error {
	amount, err := c.amount()
	if err != nil {
		return err
	}
	approver := uuid.Nil
	if *c.f.approver != "" {
		if approver, err = c.senior(ctx, *c.f.approver); err != nil {
			return err
		}
	}
	res, err := c.m.ledger.DepositTopup(ctx, c.key(), amount, admin, approver, *c.f.reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "deposit topup %s posted (replayed %t)\n", res.ID, res.Replayed)
	return nil
}

// account разбирает «вид[:разрез]»: разрез организации — идентификатор или ИНН, размещения — идентификатор.
func (c *moneyCmd) account(ctx context.Context, s string) (ledgerdomain.AccountRef, error) {
	kind, scope, _ := strings.Cut(s, ":")
	ref := ledgerdomain.AccountRef{Kind: ledgerdomain.AccountKind(kind)}
	switch {
	case ref.Kind.ScopedByOrganization() && scope != "":
		org, err := c.orgID(ctx, scope)
		if err != nil {
			return ref, err
		}
		ref.OrganizationID = org
	case ref.Kind.ScopedByPlacement() && scope != "":
		id, err := uuid.Parse(scope)
		if err != nil {
			return ref, errUsage
		}
		ref.PlacementID = id
	case scope != "":
		return ref, errUsage
	}
	if err := ref.Validate(); err != nil {
		fmt.Fprintln(c.out, err)
		return ref, errUsage
	}
	return ref, nil
}

func (c *moneyCmd) listBankTx(ctx context.Context) error {
	page, err := c.m.billing.ListBankTransactions(ctx, billingdomain.MatchStatus(*c.f.status), "", *c.f.limit)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDATE\tAMOUNT\tSTATUS\tPAYER INN\tPURPOSE")
	for _, t := range page.Items {
		purpose := innRe.ReplaceAllStringFunc(t.Purpose, logx.MaskINN)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.OperationDate.Format("2006-01-02"), rub(t.Amount), t.MatchStatus,
			logx.MaskINN(t.PayerINN), purpose)
	}
	_ = w.Flush()
	fmt.Fprintf(c.out, "%d bank transaction(s)\n", len(page.Items))
	return nil
}

func (c *moneyCmd) listLedger(ctx context.Context) error {
	filter := ledgerdomain.JournalFilter{Kind: ledgerdomain.Kind(*c.f.kind), Limit: *c.f.limit}
	if *c.f.org != "" {
		org, err := c.orgID(ctx, *c.f.org)
		if err != nil {
			return err
		}
		filter.OrganizationID = org
	}
	if *c.f.since != "" {
		t, err := time.Parse("2006-01-02", *c.f.since)
		if err != nil {
			return errUsage
		}
		filter.Since = t
	}
	lines, err := c.m.ledger.Journal(ctx, filter)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tTRANSACTION\tKIND\tDEBIT\tCREDIT\tAMOUNT\tREASON")
	for _, l := range lines {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.CreatedAt.Format("2006-01-02 15:04"), l.TransactionID, l.Kind,
			l.Debit, l.Credit, rub(l.Amount), innRe.ReplaceAllStringFunc(l.Reason, logx.MaskINN))
	}
	_ = w.Flush()
	fmt.Fprintf(c.out, "%d entr(ies)\n", len(lines))
	return nil
}

// reconcile — сверка с банком (06 §2.4): остаток bank против конечного остатка последней
// выписки, несопоставленные поступления, обязательства против денег, итоги по видам.
func (c *moneyCmd) reconcile(ctx context.Context) error {
	totals, err := c.m.ledger.Totals(ctx)
	if err != nil {
		return err
	}
	pos, err := c.m.billing.BankPosition(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT KIND\tBALANCE")
	for _, k := range []ledgerdomain.AccountKind{ledgerdomain.AccountBank, ledgerdomain.AccountRocketworkDeposit,
		ledgerdomain.AccountPayoutFees, ledgerdomain.AccountAdvertiserBalance, ledgerdomain.AccountPlacementHold,
		ledgerdomain.AccountBloggerPayable, ledgerdomain.AccountPlatformRevenue} {
		fmt.Fprintf(w, "%s\t%s\n", k, rub(totals[k]))
	}
	_ = w.Flush()
	bank := totals[ledgerdomain.AccountBank]
	if pos.Latest == nil || pos.Latest.Closing == nil {
		fmt.Fprintln(c.out, "statement: none with closing balance")
	} else {
		diff := *pos.Latest.Closing - bank
		to := "-"
		if pos.Latest.DateTo != nil {
			to = pos.Latest.DateTo.Format("2006-01-02")
		}
		fmt.Fprintf(c.out, "statement %s to %s: closing %s, ledger bank %s, difference %s\n", pos.Latest.ID, to,
			rub(*pos.Latest.Closing), rub(bank), rub(diff))
		fmt.Fprintf(c.out, "unexplained difference (minus unmatched): %s\n", rub(diff-pos.UnmatchedTotal))
	}
	fmt.Fprintf(c.out, "unmatched incoming: %d, %s\n", pos.UnmatchedCount, rub(pos.UnmatchedTotal))
	clients := totals[ledgerdomain.AccountAdvertiserBalance] + totals[ledgerdomain.AccountPlacementHold]
	cash := bank + totals[ledgerdomain.AccountRocketworkDeposit]
	fmt.Fprintf(c.out, "advertiser liabilities %s, money (bank + rocketwork deposit) %s, cover %s\n",
		rub(clients), rub(cash), rub(cash-clients))
	c.reconcileRocketWork(ctx)
	return nil
}

// rub — сумма «1234.56 RUB» без float.
func rub(k money.Kopecks) string {
	sign, v := "", int64(k)
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d RUB", sign, v/100, v%100)
}

// closingActs — ручной запуск generate_closing_acts за месяц (R-16): готовые пропускаются.
func (c *moneyCmd) closingActs(ctx context.Context) error {
	p, err := billingdomain.ParsePeriod(*c.f.period)
	if err != nil {
		fmt.Fprintln(c.out, err)
		return errUsage
	}
	res, err := c.m.billing.GenerateClosingActs(ctx, p)
	fmt.Fprintf(c.out, "closing acts %s: created %d, skipped %d, failed %d\n", p, res.Created, res.Skipped, res.Failed)
	return err
}

// reconcileRocketWork — строка сверки депозита Рокет Ворк (R-12 этапа выплат): журнал, API,
// расхождение и обязательства. Сервис недоступен или выключен — строка с причиной.
func (c *moneyCmd) reconcileRocketWork(ctx context.Context) {
	if c.m.payout == nil {
		return
	}
	r, err := c.m.payout.DepositReport(ctx)
	if err != nil {
		fmt.Fprintf(c.out, "rocketwork deposit: unavailable (%v)\n", err)
		return
	}
	fmt.Fprintf(c.out, "rocketwork deposit: ledger %s, api %s, difference %s, mismatch %t\n",
		rub(r.Ledger), rub(r.API), rub(r.API-r.Ledger), r.Mismatch())
	fmt.Fprintf(c.out, "rocketwork obligations: payable %s, in flight %s, need %s, shortage %s\n",
		rub(r.Payable), rub(r.InFlight), rub(r.Need), rub(r.Shortage()))
}
