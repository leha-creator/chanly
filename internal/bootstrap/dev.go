package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// errDevForbidden — команда разработчика вне development и test.
var errDevForbidden = errors.New("dev commands are allowed only with APP_ENV development or test")

// errDevReadyWithORD — ready в обход маркировки при включённом ОРД (R-18 этапа «Маркировка»).
var errDevReadyWithORD = errors.New("post-version ready is allowed only with ORD_ENABLED=false: use approve, the worker gets erid from ORD")

// runDev — команды разработчика до API администратора и интеграции с ОРД (R-23):
//
//	maxim dev post-version --order <номер> approve|ready|reject [--erid X] [--reason Y]
//	maxim dev balance deposit … и maxim dev statement … — деньги, см. runDevMoney
//	maxim dev rocketwork … и maxim dev placement accept … — выплаты, см. runDevPayout
//	maxim dev seed [--wait 5m] [--orders=true] — тестовые данные, см. runDevSeed
//
// Двигает текущую версию поста заказа так, как это сделает модерация (S-17): через post/app,
// поэтому размещения отвечают так же (R-5). При ORD_ENABLED=true после approve маркировка в worker
// регистрирует креатив и выдаёт erid (ordfake или тестовый контур ОРД); ready в обход маркировки —
// только при ORD_ENABLED=false (R-18 этапа «Маркировка»). Только APP_ENV development и test.
// Журнал действий пишется с актором system; moderated_by — автор заказа.
func runDev(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.Dev"
	if cfg.Env != EnvDevelopment && cfg.Env != EnvTest {
		log.ErrorContext(ctx, "dev command refused", logx.KeyOp, op, "env", cfg.Env)
		return errDevForbidden
	}
	if len(args) > 0 && (args[0] == "balance" || args[0] == "statement") {
		return runDevMoney(ctx, cfg, log, args, out)
	}
	if len(args) > 0 && (args[0] == "rocketwork" || args[0] == "placement") {
		return runDevPayout(ctx, cfg, log, args, out)
	}
	if len(args) > 0 && args[0] == "seed" {
		return runDevSeed(ctx, cfg, log, args[1:], out)
	}
	if len(args) > 0 && args[0] == "channel-metrics" {
		return runDevChannelMetrics(ctx, cfg, log, args[1:], out)
	}
	if len(args) == 0 || args[0] != "post-version" {
		return errUsage
	}
	fs := flag.NewFlagSet("dev post-version", flag.ContinueOnError)
	fs.SetOutput(out)
	number := fs.Int64("order", 0, "order number")
	erid := fs.String("erid", "", "erid for ready")
	reason := fs.String("reason", "Отклонено командой разработчика", "rejection reason")
	if err := fs.Parse(args[1:]); err != nil || *number <= 0 || fs.NArg() != 1 {
		return errUsage
	}
	action := fs.Arg(0)
	switch action {
	case "approve", "ready", "reject":
	default:
		return errUsage
	}
	if action == "ready" && cfg.ORD.Enabled {
		log.ErrorContext(ctx, "dev command refused", logx.KeyOp, op, "command", "post-version ready", "reason", "ord_enabled")
		return errDevReadyWithORD
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeDev, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	if err := m.startMarking(ctx); err != nil {
		return err
	}
	o, err := m.placementStore.Orders().GetByNumber(ctx, *number)
	if err != nil {
		return fmt.Errorf("order %d: %w", *number, err)
	}
	log.WarnContext(ctx, "dev command used", logx.KeyOp, op, "command", "post-version "+action, "order_number", o.Number)
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})
	versionID := o.CurrentPostVersionID
	switch action {
	case "approve":
		_, err = m.post.ApproveVersion(ctx, versionID, o.CreatedBy)
	case "ready":
		if *erid == "" {
			*erid = fmt.Sprintf("dev-%d", o.Number)
		}
		// Уже одобренная версия: сразу к erid.
		if _, err = m.post.ApproveVersion(ctx, versionID, o.CreatedBy); err == nil || apperr.Is(err, apperr.CodeInvalidTransition) {
			_, err = m.post.MarkVersionReady(ctx, versionID, *erid)
		}
	case "reject":
		_, err = m.post.RejectVersion(ctx, versionID, o.CreatedBy, *reason)
	}
	if err != nil {
		return err
	}
	v, err := m.placement.GetOrder(ctx, o.OrganizationID, o.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "order %d status %s version %d status %s\n", v.Order.Number, v.Order.Status, v.Version.Number, v.Version.Status)
	if action == "approve" && cfg.ORD.Enabled {
		fmt.Fprintln(out, "marking: the worker registers the creative in ORD and makes the version ready with erid")
	}
	for _, p := range v.Placements {
		fmt.Fprintf(out, "placement %s channel %s status %s\n", p.Placement.ID, p.Placement.ChannelID, p.Placement.Status)
	}
	return nil
}
