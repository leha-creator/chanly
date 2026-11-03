package bootstrap

import (
	"context"
	"fmt"

	accountapp "github.com/leha-creator/chanly/internal/account/app"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	markingdomain "github.com/leha-creator/chanly/internal/marking/domain"
	markingpg "github.com/leha-creator/chanly/internal/marking/postgres"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/stuck"
	"github.com/leha-creator/chanly/internal/platform/tx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
)

// newMarkingService собирает модуль маркировки (этап «Маркировка через Яндекс ОРД»). Адаптер
// ОРД есть только в worker: в api модуль лишь создаёт записи и задания в транзакциях сценариев.
func newMarkingService(cfg Config, in *infra, txRunner tx.Runner, queue job.Enqueuer, auditLog audit.Recorder,
	accountStore *accountpg.Store, posts *postapp.Service, channels markingdomain.ChannelPlatformReady,
	placementReports *placementpg.OrdReport) (*markingapp.Service, *markingpg.Store) {
	log := in.log.With(logx.KeyModule, "marking")
	store := markingpg.New(in.pool, log)
	postPorts := bridge.MarkingPosts{Svc: posts}
	deps := markingapp.Deps{
		Organizations: store.Organizations(), Contracts: store.Contracts(), Platforms: store.Platforms(),
		Creatives: store.Creatives(), Overview: store.Overview(),
		Reports: store.Reports(), Source: bridge.MarkingReportSource{Store: placementReports},
		Accounts: bridge.MarkingOrganizations{Orgs: accountStore.Organizations()}, Posts: postPorts, Versions: postPorts,
		Channels: channels,
		Tx:       txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: in.log,
		Config: cfg.MarkingConfig(),
	}
	if in.ord != nil {
		deps.Ord = bridge.MarkingOrd{Client: in.ord}
	}
	if in.alerter != nil {
		deps.Alerts = in.alerter
	}
	if in.reg != nil {
		deps.Metrics = markingpg.NewEridMetrics(in.reg)
	}
	return markingapp.New(deps), store
}

// wireMarking подключает маркировку к другим модулям (R-11, R-13, R-15, R-12) и проверки
// зависших объектов. При ORD_ENABLED=false не вызывается: одобрение канала сразу делает его
// видимым, версия становится ready только командой разработчика (R-18).
func (m *modules) wireMarking() {
	m.post.SetVersionListener(bridge.VersionListeners{
		bridge.VersionListener{Svc: m.placement}, bridge.MarkingVersionListener{Svc: m.marking},
	})
	m.channel.SetMarkingPorts(bridge.ChannelApprovals{Svc: m.marking})
	m.account.SetOrganizationListener(m.marking)
	m.marking.SetCreativeListener(m.placement)
	m.platform.stuck.Add(stuck.Check{Name: markingapp.StuckEridOverdue, Threshold: m.cfg.ORD.EridWait,
		Level: alert.LevelWarn, Find: m.marking.OverdueErid})
	m.platform.stuck.Add(stuck.Check{Name: markingapp.StuckSentOverdue, Threshold: m.cfg.ORD.StuckSentAge,
		Level: alert.LevelWarn, Find: m.marking.OverdueSent})
}

// startMarking — при запуске api, worker и команд: строка организации-платформы из
// конфигурации (R-5); изменённое наименование уходит в ОРД заново.
func (m *modules) startMarking(ctx context.Context) error {
	if !m.cfg.ORD.Enabled {
		return nil
	}
	inn, name, legal := m.cfg.ORDPlatform()
	id, err := m.account.EnsurePlatformOrganization(ctx, accountapp.PlatformOrg{INN: inn, Name: name, LegalType: legal})
	if err != nil {
		return fmt.Errorf("ensure platform organization: %w", err)
	}
	return m.marking.OrganizationChanged(ctx, id)
}
