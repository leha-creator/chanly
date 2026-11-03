package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"text/tabwriter"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	markingdomain "github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// errNotAdmin — у пользователя команды нет роли администратора.
var errNotAdmin = errors.New("user has no admin role")

// innRe — ИНН в тексте ошибки ОРД: в выводе команды только маскированным (07 §5.4).
var innRe = regexp.MustCompile(`\b\d{10}(\d{2})?\b`)

// runAdminORD — ошибки ОРД до API администратора (S-20, этап «Маркировка»):
//
//	maxim admin ord errors [--limit N]
//	maxim admin ord retry --object <type>:<id> --reason <основание> --email <администратор>
//	maxim admin ord report show|build|send --period YYYY-MM [--email <senior> --reason <основание>]
//
// Повтор пишется в audit_log с актором admin и основанием.
func runAdminORD(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminORD"
	if len(args) == 0 {
		return errUsage
	}
	if args[0] == "report" {
		return runAdminORDReport(ctx, cfg, log, args[1:], out)
	}
	fs := flag.NewFlagSet("admin ord "+args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		limit  = fs.Int("limit", 50, "max errors to show")
		object = fs.String("object", "", "<type>:<id>")
		reason = fs.String("reason", "", "reason of the retry")
		email  = fs.String("email", "", "admin email")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}
	var ref markingdomain.ObjectRef
	switch args[0] {
	case "errors":
		if *limit < 1 {
			return errUsage
		}
	case "retry":
		if *object == "" || *reason == "" || *email == "" {
			return errUsage
		}
		r, err := markingdomain.ParseObjectRef(*object)
		if err != nil {
			fmt.Fprintln(out, err)
			return errUsage
		}
		ref = r
	default:
		log.ErrorContext(ctx, "unknown admin ord command", logx.KeyOp, op, "command", args[0])
		return errUsage
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeAdmin, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	if args[0] == "errors" {
		page, err := m.marking.ListErrors(ctx, "", *limit)
		if err != nil {
			return err
		}
		printORDErrors(out, page.Items)
		return nil
	}
	user, err := accountpg.New(pool, log).Users().GetByEmail(ctx, accountdomain.NormalizeEmail(*email))
	if errors.Is(err, accountdomain.ErrNotFound) || (err == nil && user.AdminRole == nil) {
		log.WarnContext(ctx, "admin ord retry refused", logx.KeyOp, op, "reason", "not_admin")
		return errNotAdmin
	}
	if err != nil {
		return err
	}
	if err := m.marking.Retry(ctx, ref, user.ID, *reason); err != nil {
		return err
	}
	fmt.Fprintf(out, "ord object %s queued for retry\n", ref)
	return nil
}

// printORDErrors — таблица ошибок: вид, идентификатор, статус ОРД, попытки, ошибка (ИНН маскирован).
func printORDErrors(out io.Writer, items []markingdomain.ErrorItem) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "OBJECT\tORD STATUS\tATTEMPTS\tUPDATED\tERROR")
	for _, it := range items {
		msg := innRe.ReplaceAllStringFunc(it.Error, logx.MaskINN)
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", it.Ref, it.OrdStatus, it.Attempts, it.UpdatedAt.Format("2006-01-02 15:04"), msg)
	}
	_ = w.Flush()
	fmt.Fprintf(out, "%d object(s) in error\n", len(items))
}

// runAdminORDReport — ежемесячная отчётность ОРД из консоли (R-20…R-23): show читает
// состояние; build и send меняют данные и доступны только senior с основанием (как в API).
func runAdminORDReport(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminORDReport"
	if len(args) == 0 {
		return errUsage
	}
	action := args[0]
	fs := flag.NewFlagSet("admin ord report "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		period = fs.String("period", "", "YYYY-MM")
		reason = fs.String("reason", "", "reason of the change")
		email  = fs.String("email", "", "senior admin email")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}
	p, err := markingdomain.ParsePeriod(*period)
	if err != nil {
		fmt.Fprintln(out, err)
		return errUsage
	}
	switch action {
	case "show":
	case "build", "send":
		if *reason == "" || *email == "" {
			return errUsage
		}
	default:
		log.ErrorContext(ctx, "unknown admin ord report command", logx.KeyOp, op, "command", action)
		return errUsage
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeAdmin, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	var view *markingapp.ReportView
	switch action {
	case "show":
		view, err = m.marking.Report(ctx, p)
	default:
		user, uerr := accountpg.New(pool, log).Users().GetByEmail(ctx, accountdomain.NormalizeEmail(*email))
		if errors.Is(uerr, accountdomain.ErrNotFound) || (uerr == nil && (user.AdminRole == nil || *user.AdminRole != accountdomain.AdminSenior)) {
			log.WarnContext(ctx, "admin ord report refused", logx.KeyOp, op, "command", action, "reason", "not_senior")
			return errNotSenior
		}
		if uerr != nil {
			return uerr
		}
		if action == "build" {
			view, err = m.marking.BuildReport(ctx, p, &user.ID, *reason)
		} else {
			view, err = m.marking.SendReport(ctx, p, &user.ID, *reason)
		}
	}
	if err != nil {
		return err
	}
	printORDReport(out, view)
	return nil
}

// printORDReport — сводка отчёта: состояние, строки статистики и акты (без ИНН и имён).
func printORDReport(out io.Writer, v *markingapp.ReportView) {
	fmt.Fprintf(out, "period %s: %s, statistics %d, invoices %d, impressions %d, amount %s, pending %d, errors %d\n",
		v.Period, v.State, v.StatisticsCount, v.InvoicesCount, v.Impressions, markingdomain.Rubles(v.Amount), v.PendingCount, v.ErrorCount)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, r := range v.Invoices {
		fmt.Fprintf(w, "invoice\t%s\t%s\t%s\t%s\n", r.Invoice.Number, r.Kind, markingdomain.Rubles(r.Invoice.Amount), r.Invoice.Sync.Status)
	}
	_ = w.Flush()
}
