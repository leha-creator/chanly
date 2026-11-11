package bootstrap

import (
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	billinghttp "github.com/leha-creator/chanly/internal/billing/httpapi"
	billingpdf "github.com/leha-creator/chanly/internal/billing/pdf"
	billingpg "github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// billingHandlers — псевдоним для встраивания в apiImpl рядом с обработчиками других модулей.
type billingHandlers = billinghttp.Handlers

// newBillingService собирает модуль расчётов: счета, выписки, закрывающие документы. Деньги
// пишет журнал (bridge.BillingLedger); реквизиты — модуль аккаунтов; уведомления
// рекламодателю — модуль уведомлений (R-17).
func newBillingService(cfg Config, in *infra, txRunner tx.Runner, auditLog *audit.Log, accounts *accountpg.Store,
	ledger *ledgerapp.Service, notifier *notificationapp.Service) (*billingapp.Service, *billingpg.Store) {
	log := in.log.With(logx.KeyModule, "billing")
	store := billingpg.New(in.pool, log)
	deps := billingapp.Deps{
		InvoiceDocuments: store,
		BankIdentity:     store, BankSync: store, BankConfig: cfg.bankSyncConfig(),
		Invoices: store.Invoices(), Statements: store.Statements(), BankTransactions: store.BankTransactions(),
		ClosingActs: store.ClosingActs(), Ledger: bridge.BillingLedger{Svc: ledger},
		Notifier:      bridge.BillingNotifier{Svc: notifier},
		Organizations: bridge.BillingOrganizations{Orgs: accounts.Organizations()},
		Renderer:      billingpdf.New(in.clock, log),
		Parser:        clientbank.New(cfg.StatementMaxBytes(), log),
		Limits:        auditLog, Tx: txRunner, Audit: auditLog, Clock: in.clock, Log: in.log,
		Config: cfg.BillingAppConfig(),
	}
	if in.storage != nil {
		deps.Storage = in.storage
	}
	// Keep safe PDF download available for already issued bank invoices after a
	// switch back to local. Their persisted provider, not the current flag, wins.
	if cfg.TBank.Mode == "live" {
		issuer, err := in.bankInvoiceIssuer(cfg)
		if err != nil {
			in.log.Error("bank invoice issuer unavailable")
		} else {
			deps.InvoiceIssuer = issuer
		}
	}
	if cfg.bankAutoSyncEnabled() {
		source, err := in.bankSource(cfg, false)
		if err != nil {
			in.log.Error("bank source unavailable")
		} else {
			deps.BankSource = source
		}
	}
	if in.alerter != nil {
		deps.Alerts = in.alerter
	}
	bankConfig := cfg.bankSyncConfig()
	in.log.Info("billing configured", logx.KeyModule, "billing", "invoice_provider", cfg.Billing.InvoiceProvider, "invoice_ttl", cfg.Billing.InvoiceTTL.String(),
		"check_interval", cfg.Billing.LedgerCheckInterval.String(), "bank_mode", cfg.TBank.Mode, "bank_access", bankConfig.Access, "bank_auto_sync", cfg.bankAutoSyncEnabled())
	return billingapp.New(deps), store
}
