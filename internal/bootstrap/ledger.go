package bootstrap

import (
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerhttp "github.com/leha-creator/chanly/internal/ledger/httpapi"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// newLedgerService собирает журнал проводок (06 §2): единственный писатель ledger_* и hold.
func newLedgerService(cfg Config, in *infra, txRunner tx.Runner, auditLog audit.Recorder) (*ledgerapp.Service, *ledgerpg.Store) {
	store := ledgerpg.New(in.pool, in.log.With(logx.KeyModule, "ledger"))
	deps := ledgerapp.Deps{Store: store, Reports: store, Admin: store, Tx: txRunner, Audit: auditLog, Clock: in.clock, Log: in.log,
		ManualSecondApproval: money.Kopecks(cfg.Billing.ManualSecondApproval)}
	if in.alerter != nil {
		deps.Alerts = in.alerter
	}
	if in.reg != nil {
		deps.Metrics = ledgerpg.NewMetrics(in.reg)
	}
	return ledgerapp.New(deps), store
}

// ledgerHandlers — псевдоним для встраивания в apiImpl.
type ledgerHandlers = ledgerhttp.Handlers
