package bootstrap

import (
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	notificationhttp "github.com/leha-creator/chanly/internal/notification/httpapi"
	notificationpg "github.com/leha-creator/chanly/internal/notification/postgres"
	"github.com/leha-creator/chanly/internal/notification/render"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// notificationHandlers — псевдоним для встраивания в apiImpl.
type notificationHandlers = notificationhttp.Handlers

// newNotificationService собирает модуль уведомлений (07 §6): адресаты — модуль аккаунтов,
// каналы — модуль каналов. Бот и почта есть только в worker; в api уведомления создаются
// (кабинет и задания send_notification), а отправляет их worker.
func newNotificationService(cfg Config, in *infra, txRunner tx.Runner, queue job.Enqueuer, accounts *accountpg.Store,
	channels *channelpg.Store) (*notificationapp.Service, *notificationpg.Store) {
	log := in.log.With(logx.KeyModule, "notification")
	store := notificationpg.New(in.pool, log)
	// Шаблоны встроены в двоичный файл: ошибка разбора — ошибка сборки, её ловят тесты.
	renderer, err := render.New(cfg.CabinetBaseURL())
	if err != nil {
		panic("notification templates: " + err.Error())
	}
	deps := notificationapp.Deps{
		SettingsRepo: store, SettingsAudience: bridge.NotificationSettingsAudience{Users: accounts.Users()},
		Tx: txRunner, Jobs: queue, Repo: store,
		Recipients: bridge.NotificationRecipients{Users: accounts.Users()},
		Channels:   bridge.NotificationChannels{Channels: channels.Channels()},
		Renderer:   renderer, Clock: in.clock, Log: in.log,
		Config: notificationapp.Config{SendAttempts: cfg.Notification.SendAttempts},
	}
	if in.max != nil {
		deps.Messenger = bridge.NotificationMessenger{MAX: in.max, Log: log}
	}
	if in.mail != nil {
		deps.Mailer = in.mail
	}
	log.Info("notifications configured", "cabinet_base_url", cfg.CabinetBaseURL(), "retention", cfg.Notification.Retention.String(),
		"send_attempts", cfg.Notification.SendAttempts, "max_bot", deps.Messenger != nil, "email", deps.Mailer != nil)
	return notificationapp.New(deps), store
}
