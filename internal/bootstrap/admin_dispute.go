package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// errAdminRequired — у пользователя команды нет роли администратора (moderator или senior, 07 §4).
var errAdminRequired = errors.New("admin_required: user is not an admin")

// runAdminDispute — споры до API администратора (S-18, S-19, R-17 этапа «Приёмка, споры и возвраты»):
//
//	maxim admin dispute list [--all] [--overdue] [--limit N]
//	maxim admin dispute show --placement <id>
//	maxim admin dispute open --placement <id> --reason … --email …
//	maxim admin dispute resolve --placement <id> --resolution full_payout|proportional|full_refund [--share N] --comment … --email …
//	maxim admin dispute remove-post --placement <id> --reason … --email …
//
// Изменяющие команды — администратор moderator или senior; запись в audit_log с актором admin.
func runAdminDispute(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminDispute"
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("admin dispute "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		placement  = fs.String("placement", "", "placement id")
		reason     = fs.String("reason", "", "reason")
		comment    = fs.String("comment", "", "resolution grounds (up to 2000 characters)")
		email      = fs.String("email", "", "admin email")
		resolution = fs.String("resolution", "", "full_payout | proportional | full_refund")
		share      = fs.String("share", "", "performance share for proportional, 1…9999 basis points")
		all        = fs.Bool("all", false, "include resolved disputes")
		overdue    = fs.Bool("overdue", false, "only disputes past the T-06 deadline")
		limit      = fs.Int("limit", 50, "max rows")
	)
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
		ok = *limit > 0
	case "show":
		ok = need(placement)
	case "open", "remove-post":
		ok = need(placement, reason, email)
	case "resolve":
		ok = need(placement, resolution, comment, email)
	default:
		log.ErrorContext(ctx, "unknown admin dispute command", logx.KeyOp, op, "command", cmd)
		return errUsage
	}
	if !ok {
		return errUsage
	}
	var pid uuid.UUID
	if *placement != "" {
		var err error
		if pid, err = uuid.Parse(*placement); err != nil {
			return errUsage
		}
	}
	var sharePtr *int
	if *share != "" {
		v, err := strconv.Atoi(*share)
		if err != nil {
			return errUsage
		}
		sharePtr = &v
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
	svc := m.placement

	switch cmd {
	case "list":
		rows, err := svc.AdminDisputes(ctx, placementapp.AdminDisputeFilter{All: *all, Overdue: *overdue, Limit: *limit})
		if err != nil {
			return err
		}
		printDisputes(out, rows, in.clock.Now())
		return nil
	case "show":
		v, err := svc.AdminDispute(ctx, pid)
		if err != nil {
			return err
		}
		printDispute(out, v)
		return nil
	}

	admin, err := adminUser(ctx, accountpg.New(pool, log).Users(), log, *email)
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &admin})
	log.WarnContext(ctx, "admin dispute "+cmd, logx.KeyOp, op, KeyAdminUserID, admin, "placement_id", pid)
	switch cmd {
	case "open":
		d, err := svc.OpenDisputeByAdmin(ctx, pid, admin, *reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "dispute %s opened, deadline %s\n", d.ID, d.DeadlineAt.UTC().Format(time.RFC3339))
	case "remove-post":
		r, err := svc.RemovePostByAdmin(ctx, pid, admin, *reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "post removal queued, dispute %s (opened now: %t)\n", r.Dispute.ID, r.Opened)
	case "resolve":
		r, err := svc.ResolveDispute(ctx, placementapp.ResolveInput{PlacementID: pid,
			Resolution: placementdomain.Resolution(*resolution), Share: sharePtr, Comment: *comment, AdminID: admin})
		if err != nil {
			return err
		}
		d := r.Distribution
		fmt.Fprintf(out, "placement %s %s: share %d, payout %s, fee %s, refund %s\n", pid, r.Placement.Status,
			d.PerformanceShare, rub(d.PayoutAmount), rub(d.FeeCharged), rub(d.RefundAmount))
		if r.PayoutID != uuid.Nil {
			fmt.Fprintf(out, "payout %s created\n", r.PayoutID)
		}
		if r.RatingFrom != r.RatingTo {
			fmt.Fprintf(out, "channel rating %d -> %d\n", r.RatingFrom, r.RatingTo)
		}
		if r.RemovalScheduled {
			fmt.Fprintln(out, "post removal queued (refund)")
		}
	}
	return nil
}

// adminUser — администратор любой роли по почте (споры — moderator и senior, 07 §4; I-5).
func adminUser(ctx context.Context, users *accountpg.Users, log *slog.Logger, email string) (uuid.UUID, error) {
	user, err := users.GetByEmail(ctx, accountdomain.NormalizeEmail(email))
	if errors.Is(err, accountdomain.ErrNotFound) || (err == nil && user.AdminRole == nil) {
		log.WarnContext(ctx, "admin dispute command refused", logx.KeyOp, "Bootstrap.AdminDispute", "reason", "admin_required")
		return uuid.Nil, errAdminRequired
	}
	if err != nil {
		return uuid.Nil, err
	}
	return user.ID, nil
}

// printDisputes — таблица споров: срок, размещение, причина, кто открыл, предложение, решение.
func printDisputes(out io.Writer, rows []placementapp.AdminDisputeRow, now time.Time) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEADLINE\tPLACEMENT\tSTATUS\tREASON\tOPENED_BY\tSUGGESTED\tRESOLUTION\tOVERDUE")
	for _, r := range rows {
		d := r.Dispute
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\n", d.DeadlineAt.UTC().Format("2006-01-02 15:04"), d.PlacementID,
			r.Placement.Status, d.Reason, d.OpenedBy, share(d.SuggestedShare), orDash(string(d.Resolution)),
			placementapp.DisputeOverdue(d, now))
	}
	_ = w.Flush()
	fmt.Fprintf(out, "%d dispute(s)\n", len(rows))
}

// printDispute — спор целиком: сводка, претензия, нарушения, публикация, история, предпросмотр.
func printDispute(out io.Writer, v placementapp.AdminDisputeView) {
	p, d := v.Placement, v.Dispute
	fmt.Fprintf(out, "placement %s status %s, total %s (blogger %s, fee %s)\n", p.ID, p.Status, rub(p.TotalAmount),
		rub(p.BloggerAmount), rub(p.FeeAmount))
	fmt.Fprintf(out, "dispute %s: reason %s, opened by %s at %s, deadline %s, overdue %t\n", d.ID, d.Reason, d.OpenedBy,
		d.CreatedAt.UTC().Format(time.RFC3339), d.DeadlineAt.UTC().Format(time.RFC3339), v.Overdue)
	fmt.Fprintf(out, "suggested share: %s\n", share(d.SuggestedShare))
	if v.Preview != nil {
		fmt.Fprintf(out, "preview: payout %s, fee %s, refund %s\n", rub(v.Preview.PayoutAmount), rub(v.Preview.FeeCharged),
			rub(v.Preview.RefundAmount))
	}
	if d.Resolved() {
		fmt.Fprintf(out, "resolution: %s, share %s, at %s, comment: %s\n", d.Resolution, share(d.PerformanceShare),
			d.ResolvedAt.UTC().Format(time.RFC3339), d.ResolutionComment)
	}
	if v.Claim != nil {
		fmt.Fprintf(out, "claim %s: %s at %s\n  %s\n", v.Claim.ID, v.Claim.Reason, v.Claim.CreatedAt.UTC().Format(time.RFC3339),
			v.Claim.Text)
	}
	for _, vl := range v.Violations {
		fmt.Fprintf(out, "violation %s: %s at %s (%s)\n", vl.ID, vl.Kind, vl.OccurredAt.UTC().Format(time.RFC3339), vl.Source)
	}
	if pub := v.Publication; pub != nil {
		removed := "-"
		if pub.RemovedAt != nil {
			removed = pub.RemovedAt.UTC().Format(time.RFC3339) + " (" + string(pub.RemovalReason) + ")"
		}
		fmt.Fprintf(out, "publication %s %s: published %s, removed %s\n", pub.ID, pub.URL,
			pub.PublishedAt.UTC().Format(time.RFC3339), removed)
	}
	for _, h := range v.History {
		fmt.Fprintf(out, "  %s %s %s -> %s by %s (%s)\n", h.CreatedAt.UTC().Format("2006-01-02 15:04"), h.Transition,
			orDash(string(h.From)), h.To, h.ActorType, orDash(h.ReasonCode))
	}
}

func share(v *int) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(*v)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
