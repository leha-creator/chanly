// Package stuck — поиск зависших объектов раз в час (07 §3, R-11).
//
// Проверки регистрируют владельцы данных. Найденный объект поднимает оповещение
// stuck:<проверка> один раз; объект, который больше не находится, закрывает его.
//
// Таблица 07 §3 и где реализуется проверка:
//
//	Версия поста в approved без erid, 1 ч (T-11)        — «Маркировка через Яндекс ОРД»
//	Размещение в scheduled с прошедшим publish_at, 35 мин — «Заказ и размещения»
//	Публикация с removal_overdue, 1 ч                    — «Автопостинг и контроль»
//	Размещение в accepted без выплаты succeeded, 72 ч (T-12) — payout (StuckChecks)
//	Выплата в sent без исхода, 24 ч; в created без отправки, 2 ч — payout (StuckChecks)
//	Объект ОРД в sent, 24 ч                              — «Маркировка через Яндекс ОРД»
//	Входящее событие в received, 10 мин                  — этот этап (InboundEvents)
//	Задание в failed, сразу                              — этот этап (FailedJobs)
package stuck

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// KindCheckStuckObjects — периодическое задание проверки (07 §3: раз в час).
const KindCheckStuckObjects = "check_stuck_objects"

// Check — проверка одного вида объектов.
type Check struct {
	// Name — код проверки; причина оповещения — stuck:<Name>.
	Name      string
	Threshold time.Duration
	// Level — alert.LevelWarn или alert.LevelError (по умолчанию).
	Level string
	// Find возвращает ключи объектов, зависших дольше olderThan.
	Find func(ctx context.Context, olderThan time.Time) ([]string, error)
}

// Alerts — то, что проверке нужно от platform/alert.
type Alerts interface {
	Raise(ctx context.Context, a alert.Alert) error
	Resolve(ctx context.Context, reason, objectKey string) error
	OpenKeys(ctx context.Context, reason string) ([]string, error)
}

// Checker выполняет зарегистрированные проверки.
type Checker struct {
	alerts Alerts
	clock  clock.Clock
	log    *slog.Logger
	checks []Check
}

// New создаёт Checker без проверок.
func New(alerts Alerts, clk clock.Clock, log *slog.Logger) *Checker {
	return &Checker{alerts: alerts, clock: clk, log: log}
}

// Add регистрирует проверку.
func (c *Checker) Add(ch Check) { c.checks = append(c.checks, ch) }

// Register регистрирует периодическое задание check_stuck_objects.
func (c *Checker) Register(w *job.Worker) {
	w.RegisterPeriodic(KindCheckStuckObjects, job.Every(time.Hour), c.Run, job.HandlerOptions{})
}

// Run выполняет все проверки. Ошибка одной проверки не останавливает остальные.
func (c *Checker) Run(ctx context.Context, _ job.Running) error {
	var failed []string
	for _, ch := range c.checks {
		if err := c.run(ctx, ch); err != nil {
			c.log.ErrorContext(ctx, "stuck objects check failed", logx.KeyOp, "Stuck.Run", "check", ch.Name, logx.KeyError, err.Error())
			failed = append(failed, ch.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("stuck checks failed: %v", failed)
	}
	return nil
}

func (c *Checker) run(ctx context.Context, ch Check) error {
	reason := "stuck:" + ch.Name
	found, err := ch.Find(ctx, c.clock.Now().Add(-ch.Threshold))
	if err != nil {
		return err
	}
	open, err := c.alerts.OpenKeys(ctx, reason)
	if err != nil {
		return err
	}
	raised := 0
	for _, key := range found {
		if slices.Contains(open, key) {
			continue // уже оповещали — не повторяем каждый час
		}
		if err := c.alerts.Raise(ctx, alert.Alert{
			Reason: reason, ObjectKey: key, Level: ch.Level,
			Summary: fmt.Sprintf("Object is stuck longer than %s: %s", ch.Threshold, ch.Name),
			Fields:  map[string]any{"check": ch.Name, "object": key, "threshold": ch.Threshold.String()},
		}); err != nil {
			return err
		}
		raised++
	}
	for _, key := range open {
		if !slices.Contains(found, key) {
			if err := c.alerts.Resolve(ctx, reason, key); err != nil {
				return err
			}
		}
	}
	c.log.InfoContext(ctx, "stuck objects check finished", logx.KeyOp, "Stuck.Run", "check", ch.Name,
		"found", len(found), "raised", raised)
	return nil
}
