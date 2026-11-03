package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// errDevRocketWorkFake — команды подделки Рокет Ворк без ROCKETWORK_MODE=fake.
var errDevRocketWorkFake = errors.New("dev rocketwork commands require ROCKETWORK_MODE=fake")

// runDevPayout — команды разработчика этапа выплат (R-4, только development и test):
//
//	maxim dev rocketwork invite --org <id|ИНН>
//	maxim dev rocketwork activate --org <id|ИНН> [--webhook <url>]
//	maxim dev rocketwork block --org <id|ИНН> [--reason blocked|tax_status_lost] [--webhook <url>]
//	maxim dev rocketwork deposit --amount <руб.коп>
//	maxim dev rocketwork complete --payout <id> [--fee <руб.коп>] [--webhook <url>]
//	maxim dev rocketwork fail --payout <id> [--reason <код сервиса>] [--message …] [--webhook <url>]
//	maxim dev placement accept --placement <id> [--share <б.п., 10000 = 100 %>]
//
// Подделка хранит состояние в ROCKETWORK_FAKE_STATE_PATH, общем с api и worker. Без --webhook
// изменение применяется сразу (как обработчик события); с --webhook на адрес api уходит
// подписанный вебхук, как его отправит сервис.
func runDevPayout(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.DevPayout"
	if len(args) < 2 {
		return errUsage
	}
	group, cmd := args[0], args[1]
	fs := flag.NewFlagSet("dev "+group+" "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	org := fs.String("org", "", "organization id or INN")
	payout := fs.String("payout", "", "payout id")
	placement := fs.String("placement", "", "placement id")
	share := fs.Int("share", 10000, "performance share, basis points")
	amount := fs.String("amount", "", "rubles.kopecks")
	fee := fs.String("fee", "", "service fee, rubles.kopecks (default — fake tariff)")
	reason := fs.String("reason", "", "reason code")
	message := fs.String("message", "Отклонено командой разработчика", "failure message")
	webhook := fs.String("webhook", "", "api webhook URL, e.g. http://localhost:8080/api/v1/webhooks/rocketwork")
	if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	switch group + " " + cmd {
	case "rocketwork invite", "rocketwork activate", "rocketwork block":
		if *org == "" {
			return errUsage
		}
	case "rocketwork deposit":
		if *amount == "" {
			return errUsage
		}
	case "rocketwork complete", "rocketwork fail":
		if *payout == "" {
			return errUsage
		}
	case "placement accept":
		if *placement == "" {
			return errUsage
		}
	default:
		return errUsage
	}
	if group == "rocketwork" && cfg.RocketWorkMode() != RocketWorkFake {
		return errDevRocketWorkFake
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
	log.WarnContext(ctx, "dev command used", logx.KeyOp, op, "command", group+" "+cmd)
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})

	if group == "placement" {
		id, err := uuid.Parse(*placement)
		if err != nil {
			return errUsage
		}
		payoutID, err := m.placement.DevAccept(ctx, id, *share)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "placement %s accepted, payout %s\n", id, payoutID)
		return nil
	}

	fake, ok := in.rocketwork.(*rocketworkfake.Fake)
	if !ok {
		return errDevRocketWorkFake
	}
	mc := &moneyCmd{m: m, log: log, out: out}
	switch cmd {
	case "invite":
		orgID, err := mc.orgID(ctx, *org)
		if err != nil {
			return err
		}
		if _, err := m.payout.Invite(ctx, orgID); err != nil {
			return err
		}
		fmt.Fprintln(out, "test contractor invited; worker will register it")
		return nil
	case "deposit":
		v, err := money.Parse(*amount)
		if err != nil || v < 0 {
			return errUsage
		}
		if err := fake.SetDeposit(int64(v)); err != nil {
			return err
		}
		fmt.Fprintf(out, "rocketwork deposit %s\n", rub(v))
		return nil
	case "activate", "block":
		orgID, err := mc.orgID(ctx, *org)
		if err != nil {
			return err
		}
		c, err := m.payoutStore.ContractorByOrg(ctx, orgID, false)
		if err != nil {
			return fmt.Errorf("contractor of %s: %w", orgID, err)
		}
		if c.ExternalID == "" {
			return fmt.Errorf("contractor of %s is not registered yet: run the worker (ensure_contractor)", orgID)
		}
		if cmd == "activate" {
			err = fake.ActivateContractor(c.ExternalID)
		} else {
			r := *reason
			if r == "" {
				r = "blocked"
			}
			err = fake.BlockContractor(c.ExternalID, r)
		}
		if err != nil {
			return err
		}
		if *webhook != "" {
			return devSendWebhook(ctx, fake, *webhook, rocketwork.EventContractorStatus, c.ExternalID, out)
		}
		if err := m.payout.ApplyContractor(ctx, c.ExternalID); err != nil {
			return err
		}
		fmt.Fprintf(out, "contractor %s %s\n", c.ExternalID, cmd)
		return nil
	}
	id, err := uuid.Parse(*payout)
	if err != nil {
		return errUsage
	}
	p, err := m.payout.Payout(ctx, id)
	if err != nil {
		return err
	}
	if p.ExternalID == "" {
		return fmt.Errorf("payout %s is not sent yet (status %s): run the worker (send_payout)", p.ID, p.Status)
	}
	if cmd == "complete" {
		f := int64(-1)
		if *fee != "" {
			v, err := money.Parse(*fee)
			if err != nil || v < 0 {
				return errUsage
			}
			f = int64(v)
		}
		err = fake.CompletePayout(p.ExternalID, f)
	} else {
		r := *reason
		if r == "" {
			r = "rejected"
		}
		err = fake.FailPayout(p.ExternalID, r, *message)
	}
	if err != nil {
		return err
	}
	if *webhook != "" {
		return devSendWebhook(ctx, fake, *webhook, rocketwork.EventPaymentStatus, p.ExternalID, out)
	}
	if err := m.payout.ApplyPayout(ctx, p.ExternalID); err != nil {
		return err
	}
	if p, err = m.payout.Payout(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(out, "payout %s %s\n", p.ID, p.Status)
	return nil
}

// devSendWebhook отправляет подписанный вебхук подделки на api.
func devSendWebhook(ctx context.Context, fake *rocketworkfake.Fake, url, kind, objectID string, out io.Writer) error {
	h, body := fake.SignedWebhook(kind, objectID)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(url), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = h
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	fmt.Fprintf(out, "webhook %s %s: %d\n", kind, objectID, resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook answered %d", resp.StatusCode)
	}
	return nil
}
