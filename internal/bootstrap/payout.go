package bootstrap

import (
	"net/http"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	payouthttp "github.com/leha-creator/chanly/internal/payout/httpapi"
	payoutpg "github.com/leha-creator/chanly/internal/payout/postgres"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
	checkpg "github.com/leha-creator/chanly/internal/rocketworkcheck/postgres"
)

// rocketworkWebhookPattern — маршрут вебхука Рокет Ворк (операция rocketworkWebhook, R-11):
// сырое тело и подпись, вне strict server.
const rocketworkWebhookPattern = "POST " + BaseURL + "/webhooks/rocketwork"

// payoutHandlers — псевдоним для встраивания в apiImpl рядом с обработчиками других модулей.
type payoutHandlers = payouthttp.Handlers

// newPayoutService собирает модуль выплат (этап «Выплаты блогерам через Рокет Ворк»).
// Порты: размещения и P-16 — bridge.PayoutPlacements, проводки — bridge.PayoutLedger,
// организации — account, Рокет Ворк — адаптер infra (R-1), уведомления — bridge.PayoutNotifier
// (R-15). После сборки модуль подключается к размещениям: гейт исполнителя P-03 и создание
// выплаты при переходе в accepted (I-1).
func newPayoutService(cfg Config, in *infra, txRunner tx.Runner, queue job.Scheduler, auditLog *audit.Log,
	accounts *accountpg.Store, ledger *ledgerapp.Service, placements *placementapp.Service,
	placementStore *placementpg.Store, notifier *notificationapp.Service) (*payoutapp.Service, *payoutpg.Store) {
	log := in.log.With(logx.KeyModule, "payout")
	store := payoutpg.New(in.pool, log)
	api := in.rocketwork
	if api == nil {
		api = rocketwork.NewDisabled(log, in.clock.Now)
	}
	deps := payoutapp.Deps{
		Store: store, Provider: bridge.PayoutProvider{API: api, Log: log},
		Ledger:        bridge.PayoutLedger{Svc: ledger},
		Placements:    bridge.PayoutPlacements{Svc: placements, Placements: placementStore.Placements()},
		Organizations: bridge.PayoutOrganizations{Orgs: accounts.Organizations()},
		Tx:            txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: in.log,
		Limits:   auditLog,
		Notifier: bridge.PayoutNotifier{Svc: notifier},
		Config:   cfg.PayoutAppConfig(),
	}
	if cfg.RocketWorkMode() == RocketWorkLive && cfg.RocketWork.Target == "production" {
		if client, ok := api.(*rocketwork.Client); ok {
			deps.DiagnosticEvents = bridge.CheckEventRouter{Events: checkapp.Events{Registry: checkpg.New(in.pool, log), Log: log}, AccountAlias: cfg.RocketWork.AccountKey, API: client}
		}
	}
	if in.alerter != nil {
		deps.Alerts = in.alerter
	}
	if in.reg != nil {
		deps.Metrics = payoutpg.NewMetrics(in.reg)
		in.reg.MustRegister(payoutpg.NewCollector(store))
	}
	svc := payoutapp.New(deps)
	placements.SetPayoutPorts(svc, bridge.PlacementPayouts{Svc: svc})
	return svc, store
}

// newRocketWorkWebhook — приём вебхука Рокет Ворк (R-11): подпись и запись inbound_event.
func newRocketWorkWebhook(in *infra, svc *payoutapp.Service, txRunner tx.Runner, queue job.Enqueuer, trusted httpx.Middleware) http.Handler {
	rec := inbound.NewRecorder(in.pool, txRunner, queue, in.clock, in.log.With(logx.KeyModule, "inbound"))
	var reg payouthttp.Registerer
	if in.reg != nil {
		reg = in.reg
	}
	var alerts payouthttp.Alerter
	if in.alerter != nil {
		alerts = in.alerter
	}
	return httpx.Chain(payouthttp.NewWebhook(svc, rec, alerts, in.clock, in.log, reg), trusted)
}
