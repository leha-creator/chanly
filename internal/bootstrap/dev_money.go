package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var msk = time.FixedZone("MSK", 3*60*60)

// devPayment — поступление в выписке разработки.
type devPayment struct {
	number  string
	amount  money.Kopecks
	inn     string
	purpose string
}

// devStatementFile — выписка «1С» в UTF-8 по счёту account за день date (05 §5) с конечным
// остатком closing (nil — без раздела остатков): то, что администратор загрузит командой
// admin money statement import.
func devStatementFile(account string, date time.Time, closing *money.Kopecks, payments ...devPayment) []byte {
	d := date.In(msk).Format("02.01.2006")
	var b strings.Builder
	fmt.Fprintf(&b, "1CClientBankExchange\nВерсияФормата=1.03\nКодировка=UTF-8\nОтправитель=maxim dev %s\n", date.UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "ДатаНачала=%s\nДатаКонца=%s\nРасчСчет=%s\n", d, d, account)
	if closing != nil {
		fmt.Fprintf(&b, "СекцияРасчСчет\nРасчСчет=%s\nКонечныйОстаток=%d.%02d\nКонецРасчСчет\n", account, *closing/100, *closing%100)
	}
	for _, p := range payments {
		fmt.Fprintf(&b, "СекцияДокумент=Платёжное поручение\nНомер=%s\nДата=%s\nСумма=%d.%02d\n", p.number, d, p.amount/100, p.amount%100)
		fmt.Fprintf(&b, "ПлательщикСчет=40702810500000000004\nПлательщикИНН=%s\nПлательщик=Плательщик (разработка)\n", p.inn)
		fmt.Fprintf(&b, "ПолучательСчет=%s\nДатаПоступило=%s\nНазначениеПлатежа=%s\nКонецДокумента\n", account, d, p.purpose)
	}
	b.WriteString("КонецФайла\n")
	return []byte(b.String())
}

// runDevMoney — деньги для ручных прогонов (замена _mock/invoices/{id}/pay):
//
//	maxim dev balance deposit --email <пользователь рекламодателя> --amount <руб.коп>
//	maxim dev statement --invoice MX-… [--amount <руб.коп>] [--out <путь>]
//
// deposit выставляет счёт организации пользователя и загружает выписку с оплатой по нему —
// тот же путь, что у администратора (S-06, S-30). statement печатает файл выписки с
// поступлением по счёту (в файл --out или в вывод команды) для maxim admin money statement import.
func runDevMoney(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.DevMoney"
	cmd := args[0]
	rest := args[1:]
	if cmd == "balance" {
		if len(rest) == 0 || rest[0] != "deposit" {
			return errUsage
		}
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("dev "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "advertiser user email")
	amountStr := fs.String("amount", "", "rubles.kopecks")
	number := fs.String("invoice", "", "invoice number MX-…")
	outPath := fs.String("out", "", "statement file path (default: stdout)")
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	var amount money.Kopecks
	if *amountStr != "" {
		v, err := money.Parse(*amountStr)
		if err != nil || v <= 0 {
			return errUsage
		}
		amount = v
	}
	switch {
	case cmd == "balance" && (*email == "" || amount == 0):
		return errUsage
	case cmd == "statement" && *number == "":
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
	account := cfg.BillingSupplier().BankAccount
	log.WarnContext(ctx, "dev command used", logx.KeyOp, op, "command", cmd)

	if cmd == "statement" {
		inv, err := m.billing.InvoiceByNumber(ctx, *number)
		if err != nil {
			return err
		}
		if amount == 0 {
			amount = inv.Amount
		}
		file := devStatementFile(account, in.clock.Now(), nil,
			devPayment{number: strings.TrimPrefix(inv.Number, "MX-"), amount: amount, inn: inv.Payer.INN,
				purpose: "Пополнение баланса по счёту № " + inv.Number + ". Без НДС"})
		if *outPath == "" {
			_, err = out.Write(file)
			return err
		}
		if err := os.WriteFile(*outPath, file, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "statement for invoice %s written to %s\n", inv.Number, *outPath)
		return nil
	}

	accounts := accountpg.New(pool, log)
	user, err := accounts.Users().GetByEmail(ctx, accountdomain.NormalizeEmail(*email))
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	org, err := accounts.Organizations().GetByUser(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("organization: %w", err)
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdvertiser, UserID: &user.ID, OrganizationID: &org.ID})
	inv, err := m.billing.CreateInvoice(ctx, org.ID, user.ID, amount, nil)
	if err != nil {
		return err
	}
	file := devStatementFile(account, in.clock.Now(), nil, devPayment{number: strings.TrimPrefix(inv.Number, "MX-"), amount: amount,
		inn: inv.Payer.INN, purpose: "Пополнение баланса по счёту № " + inv.Number})
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})
	res, err := m.billing.ImportStatement(ctx, user.ID, file)
	if err != nil {
		return err
	}
	available, held, err := m.ledger.Balance(ctx, org.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "invoice %s paid: credited %s, available %s, held %s (matched %d)\n", inv.Number, rub(res.Credited),
		rub(available), rub(held), res.Matched)
	return nil
}
