package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// runAdminPayout — выплаты до API администратора (S-19, R-3, R-9 этапа выплат):
//
//	maxim admin payout list [--status created|sent|succeeded|failed] [--org <id|ИНН>] [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--limit N]
//	maxim admin payout retry --payout <id> --reason … --email …
//	maxim admin payout mark-paid --payout <id> --bank-ref <п/п> --reason … --email … [--approver-email …]
//	maxim admin payout contractor refresh --org <id|ИНН>
//
// Изменяющие команды требуют администратора senior и основание; запись в audit_log.
func runAdminPayout(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminPayout"
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	if cmd == "contractor" {
		if len(rest) == 0 || rest[0] != "refresh" {
			return errUsage
		}
		cmd, rest = "contractor refresh", rest[1:]
	}
	fs := flag.NewFlagSet("admin payout "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	f := struct {
		payout, bankRef, reason, email, approver, status, org, from, to *string
		limit                                                           *int
	}{
		payout: fs.String("payout", "", "payout id"), bankRef: fs.String("bank-ref", "", "payment order reference"),
		reason: fs.String("reason", "", "reason"), email: fs.String("email", "", "senior admin email"),
		approver: fs.String("approver-email", "", "second senior admin email"),
		status:   fs.String("status", "", "created | sent | succeeded | failed"), org: fs.String("org", "", "organization id or INN"),
		from: fs.String("from", "", "YYYY-MM-DD"), to: fs.String("to", "", "YYYY-MM-DD"), limit: fs.Int("limit", 50, "max rows"),
	}
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
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
	case "list":
		ok = *f.limit > 0
	case "retry":
		ok = need(f.payout, f.reason, f.email)
	case "mark-paid":
		ok = need(f.payout, f.bankRef, f.reason, f.email)
	case "contractor refresh":
		ok = need(f.org)
	default:
		log.ErrorContext(ctx, "unknown admin payout command", logx.KeyOp, op, "command", cmd)
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
	in, err := newInfra(ctx, cfg, ModeAdmin, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	c := &moneyCmd{m: m, users: accountpg.New(pool, log).Users(), log: log, out: out}

	switch cmd {
	case "list":
		filter := payoutdomain.AdminFilter{Status: payoutdomain.Status(*f.status), Limit: *f.limit}
		if *f.org != "" {
			if filter.OrganizationID, err = c.orgID(ctx, *f.org); err != nil {
				return err
			}
		}
		for _, d := range []struct {
			s   string
			dst *time.Time
		}{{*f.from, &filter.From}, {*f.to, &filter.To}} {
			if d.s == "" {
				continue
			}
			t, err := time.ParseInLocation("2006-01-02", d.s, time.UTC)
			if err != nil {
				return errUsage
			}
			*d.dst = t
		}
		if !filter.To.IsZero() {
			filter.To = filter.To.Add(24 * time.Hour)
		}
		items, err := m.payout.AdminList(ctx, filter)
		if err != nil {
			return err
		}
		printPayouts(out, items)
		return nil
	case "contractor refresh":
		org, err := c.orgID(ctx, *f.org)
		if err != nil {
			return err
		}
		v, err := m.payout.Refresh(ctx, org)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "contractor of %s: %s, refresh queued\n", org, v.Status)
		return nil
	}

	admin, err := c.senior(ctx, *f.email)
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &admin})
	id, err := uuid.Parse(*f.payout)
	if err != nil {
		return errUsage
	}
	switch cmd {
	case "retry":
		next, err := m.payout.Retry(ctx, id, admin, *f.reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "payout %s attempt %d created\n", next.ID, next.Attempt)
	case "mark-paid":
		p, err := m.payout.Payout(ctx, id)
		if err != nil {
			return err
		}
		approver := uuid.Nil
		if *f.approver != "" {
			if approver, err = c.senior(ctx, *f.approver); err != nil {
				return err
			}
		}
		if limit := money.Kopecks(cfg.Billing.ManualSecondApproval); limit > 0 && p.Amount > limit && (approver == uuid.Nil || approver == admin) {
			log.WarnContext(ctx, "admin payout mark paid refused", logx.KeyOp, op, KeyAdminUserID, admin,
				"amount", int64(p.Amount), "reason", "second_approval")
			return ledgerapp.ErrSecondApproval
		}
		if p, err = m.payout.MarkLegalPaid(ctx, id, admin, approver, *f.bankRef, *f.reason); err != nil {
			return err
		}
		fmt.Fprintf(out, "payout %s %s, %s\n", p.ID, p.Status, rub(p.Amount))
	}
	return nil
}

// KeyAdminUserID — поле журнала: администратор команды.
const KeyAdminUserID = "admin_user_id"

// printPayouts — таблица выплат: дата, организация, размещение, попытка, сумма, состояние, код.
func printPayouts(out io.Writer, items []payoutdomain.Payout) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CREATED\tPAYOUT\tORGANIZATION\tPLACEMENT\tATTEMPT\tMETHOD\tAMOUNT\tSTATUS\tFAILURE")
	for _, p := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", p.CreatedAt.Format("2006-01-02 15:04"), p.ID,
			p.BloggerOrganizationID, p.PlacementID, p.Attempt, p.Method, rub(p.Amount), p.Status, p.FailureCode)
	}
	_ = w.Flush()
	fmt.Fprintf(out, "%d payout(s)\n", len(items))
}
