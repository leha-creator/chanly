package bootstrap

import (
	adminapp "github.com/leha-creator/chanly/internal/admin/app"
	admindomain "github.com/leha-creator/chanly/internal/admin/domain"
	adminhttp "github.com/leha-creator/chanly/internal/admin/httpapi"
	adminpg "github.com/leha-creator/chanly/internal/admin/postgres"
	billinghttp "github.com/leha-creator/chanly/internal/billing/httpapi"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	ledgerhttp "github.com/leha-creator/chanly/internal/ledger/httpapi"
	markinghttp "github.com/leha-creator/chanly/internal/marking/httpapi"
	payouthttp "github.com/leha-creator/chanly/internal/payout/httpapi"
	placementhttp "github.com/leha-creator/chanly/internal/placement/httpapi"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// adminHandlers — псевдоним для встраивания в apiImpl.
type adminHandlers = adminhttp.Handlers

// markingHandlers — псевдоним для встраивания в apiImpl.
type markingHandlers = markinghttp.Handlers

// wireAdmin собирает модуль admin (этап «API администратора»): второе подтверждение с
// исполнителями ledger и payout (R-2, R-3), журнал действий, счётчики меню; подключает порты
// ответов администратора модулей: ссылки на организации и пользователей, выплаты размещения.
func (m *modules) wireAdmin() {
	log := m.in.log.With(logx.KeyModule, "admin")
	refs := adminref.New(m.in.pool)
	store := adminpg.New(m.in.pool, log)
	deps := adminapp.Deps{Store: store, Audits: store, Audit: audit.New(m.in.pool, m.in.clock, log), Tx: m.txRunner,
		Clock: m.in.clock, Log: m.in.log, TTL: m.cfg.Billing.ApprovalTTL,
		Counters: []adminapp.Counter{
			bridge.ChannelCounter(m.channel), bridge.PlacementCounter(m.placement), bridge.BillingCounter(m.billing),
			bridge.PayoutCounter(m.payoutStore), bridge.MarkingCounter(m.markingStore.Overview()),
		}}
	if m.cfg.ORD.Enabled {
		deps.Counters = append(deps.Counters, bridge.OrdReportCounter(m.marking))
	}
	if m.in.reg != nil {
		deps.Metrics = adminpg.NewMetrics(m.in.reg)
	}
	svc := adminapp.New(deps)
	svc.SetExecutor(admindomain.KindLedgerManual, bridge.AdminLedgerManual{Svc: m.ledger})
	svc.SetExecutor(admindomain.KindDepositTopup, bridge.AdminDepositTopup{Svc: m.ledger})
	svc.SetExecutor(admindomain.KindPayoutMarkPaid, bridge.AdminPayoutMarkPaid{Svc: m.payout})
	m.admin, m.adminHTTP = svc, adminhttp.New(svc, refs, m.in.log)

	placementPorts := placementhttp.AdminPorts{Refs: refs, Payouts: m.payoutHTTP}
	if m.cfg.ORD.Enabled {
		// Статус ОРД в карточках: креатив версии поста, контрагент организации.
		placementPorts.Creatives = m.markingHTTP
		m.accountHTTP.SetOrdCounterparty(m.markingHTTP)
	}
	m.placementHTTP.SetAdminPorts(placementPorts)
	m.ledgerHTTP.SetAdminPorts(ledgerhttp.AdminPorts{Approvals: bridge.LedgerApprovals{H: m.adminHTTP},
		Bank: bridge.LedgerBankStatement{Svc: m.billing}, Refs: refs})
	m.payoutHTTP.SetAdminPorts(payouthttp.AdminPorts{Approvals: bridge.PayoutApprovals{H: m.adminHTTP}, Refs: refs})
	m.billingHTTP.SetAdminPorts(billinghttp.AdminPorts{Refs: refs, StatementMaxBytes: m.cfg.StatementMaxBytes()})
}
