package bootstrap

import (
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	placementhttp "github.com/leha-creator/chanly/internal/placement/httpapi"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/stuck"
	"github.com/leha-creator/chanly/internal/platform/tx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
)

// placementHandlers — псевдоним для встраивания в apiImpl рядом с обработчиками других модулей.
type placementHandlers = placementhttp.Handlers

// stuckScheduledPlacement — проверка зависших размещений в scheduled (07 §3).
const stuckScheduledPlacement = "placement_scheduled_overdue"

// stuckRemovalOverdue — проверка публикаций, которые не удалось снять по сроку (07 §3).
const stuckRemovalOverdue = "publication_removal_overdue"

// newPlacementService собирает модуль размещений. Порты других модулей — переходники
// пакета bridge (удержания — журнал проводок); порты будущих модулей — пустые реализации с
// метками этапов (R-3):
//
//	ContractorGate, Payouts — AllowAllContractors и NoPayouts до SetPayoutPorts: модуль выплат
//	                          собирается после размещений (newPayoutService, I-1 этапа выплат)
//
// Уведомления сторонам — модуль уведомлений (bridge.PlacementNotifier).
//
// Реализации модуля размещений подставляются в модули каналов и post (R-5, S-25, R-8).
func newPlacementService(cfg Config, in *infra, txRunner tx.Runner, queue job.Scheduler, auditLog audit.Recorder,
	channels *channelapp.Service, posts *postapp.Service, ledger *ledgerapp.Service, notifier *notificationapp.Service) (*placementapp.Service, *placementpg.Store) {
	log := in.log.With(logx.KeyModule, "placement")
	store := placementpg.New(in.pool, log)
	p := cfg.Placement
	deps := placementapp.Deps{
		Carts: store.Carts(), Orders: store.Orders(), Placements: store.Placements(), History: store.History(),
		Publications: store.Publications(), Violations: store.Violations(), Disputes: store.Disputes(),
		Claims: store.Claims(), Ratings: bridge.PlacementChannelRatings{Svc: channels},
		Admin: store.Admin(), Messages: store.DisputeMessages(),
		Moderation: bridge.PlacementPostModeration{Svc: posts, MarkingOff: !cfg.ORD.Enabled},
		Content:    bridge.PlacementContent{Svc: posts},
		Channels:   bridge.PlacementChannels{Svc: channels}, Campaigns: bridge.PlacementCampaigns{Svc: posts},
		Versions:    bridge.PlacementVersions{Svc: posts},
		Holds:       bridge.LedgerHolds{Svc: ledger},
		Contractors: bridge.AllowAllContractors{Log: log},
		Payouts:     bridge.NoPayouts{},
		Notifier:    bridge.PlacementNotifier{Svc: notifier},
		Tx:          txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: in.log,
		Config: placementapp.Config{
			FeeRateBP: cfg.Channel.FeeRateBP,
			Timing: domain.Timing{
				AcceptTimeout: p.AcceptTimeout, ScheduleTimeout: p.ScheduleTimeout, WindowGuard: p.WindowGuard,
				MinLead: p.MinLead, SlotStep: p.SlotStep, ReeditGuard: p.ReeditGuard,
			},
			Cart:    domain.CartRules{MaxItems: p.CartMaxItems},
			Control: controlRules(p.Control),
		},
	}
	if in.max != nil {
		// Публикация и контроль — только в worker: в api адаптера MAX нет (07 §1).
		deps.Publisher = bridge.PlacementPublisher{API: in.max}
	}
	if in.alerter != nil {
		deps.Alerts = in.alerter
	}
	if in.reg != nil {
		deps.Metrics = placementpg.NewControlMetrics(in.reg)
	}
	svc := placementapp.New(deps)
	posts.SetVersionListener(bridge.VersionListener{Svc: svc})
	link := bridge.ChannelPlacements{Svc: svc}
	channels.SetPlacementPorts(link, link, link)
	channels.SetPostListener(bridge.ChannelPosts{Svc: svc})
	return svc, store
}

// controlRules — правила публикации, снятия и контроля из конфигурации (07 §8).
func controlRules(c PlacementControlConfig) domain.ControlRules {
	return domain.ControlRules{
		PublishRetryWindow: c.PublishRetryWindow, PublishRetryInterval: c.PublishRetryInterval,
		PublishFindLookback: c.PublishFindLookback, MaxImages: c.PublishMaxImages, RemoveRetryWindow: c.RemoveRetryWindow,
		ClaimWindow: c.ClaimWindow, DisputeDeadline: c.DisputeDeadline, TopViolationShare: c.TopViolationShare,
		RatingPenaltyRefund: c.RatingPenaltyRefund, RatingPenaltyProportional: c.RatingPenaltyProportional,
		ReconcileInterval: c.ReconcileInterval, ScheduledGrace: c.ScheduledGrace, ViewsRefresh: c.ViewsRefresh,
	}
}

// stuckRemovalCheck — публикация с removal_overdue дольше порога (07 §3).
func stuckRemovalCheck(cfg Config, svc *placementapp.Service) stuck.Check {
	return stuck.Check{Name: stuckRemovalOverdue, Threshold: cfg.Placement.Control.StuckRemovalOverdueAge,
		Level: alert.LevelWarn, Find: svc.OverdueRemovals}
}

// stuckScheduledCheck — размещение в scheduled дольше порога после publish_at (07 §3).
func stuckScheduledCheck(cfg Config, svc *placementapp.Service) stuck.Check {
	return stuck.Check{Name: stuckScheduledPlacement, Threshold: cfg.Placement.StuckScheduledAge, Level: alert.LevelError,
		Find: svc.OverdueScheduled}
}
