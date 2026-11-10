package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// runAdmin — служебная команда ролей администратора до API администратора (R-2 этапа «Каталог»):
//
//	maxim admin grant --email <почта> --role moderator|senior
//	maxim admin revoke --email <почта>
//	maxim admin ord errors|retry … — ошибки ОРД (S-20, этап «Маркировка»), см. runAdminORD
//	maxim admin money … — деньги (этап «Приём оплаты и учёт денег»), см. runAdminMoney
//	maxim admin payout … — выплаты блогерам (этап «Выплаты блогерам через Рокет Ворк»), см. runAdminPayout
//	maxim admin dispute … — споры (этап «Приёмка, споры и возвраты»), см. runAdminDispute
//	maxim admin pd export|anonymize … — запросы субъекта ПДн (этап «API администратора»), см. runAdminPD
//
// Пользователя без учётной записи grant создаёт. Действие пишется в audit_log с актором system.
func runAdmin(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	if args[0] == "ord" {
		return runAdminORD(ctx, cfg, log, args[1:], out)
	}
	if args[0] == "money" {
		return runAdminMoney(ctx, cfg, log, args[1:], out)
	}
	if args[0] == "payout" {
		return runAdminPayout(ctx, cfg, log, args[1:], out)
	}
	if args[0] == "rocketwork" {
		return runAdminRocketWork(ctx, cfg, log, args[1:], out)
	}
	if args[0] == "dispute" {
		return runAdminDispute(ctx, cfg, log, args[1:], out)
	}
	if args[0] == "pd" {
		return runAdminPD(ctx, cfg, log, args[1:], out)
	}
	var role *domain.AdminRole
	fs := flag.NewFlagSet("admin "+args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "user email")
	switch args[0] {
	case "grant":
		r := fs.String("role", "", "moderator | senior")
		if err := fs.Parse(args[1:]); err != nil || *email == "" || *r == "" {
			return errUsage
		}
		v := domain.AdminRole(*r)
		if v != domain.AdminModerator && v != domain.AdminSenior {
			return errUsage
		}
		role = &v
	case "revoke":
		if err := fs.Parse(args[1:]); err != nil || *email == "" {
			return errUsage
		}
	default:
		log.ErrorContext(ctx, "unknown admin command", logx.KeyOp, "Bootstrap.Admin", "command", args[0])
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
	res, err := newModules(cfg, in).account.SetAdminRole(ctx, *email, role)
	if err != nil {
		return err
	}
	name := "none"
	if role != nil {
		name = string(*role)
	}
	fmt.Fprintf(out, "user %s role %s created %t changed %t\n", res.UserID, name, res.Created, res.Changed)
	return nil
}
