// Package cleanup — ежесуточное задание cleanup (07 §2): удаление устаревших
// записей служебных таблиц и таблиц модулей. Что и с каким сроком удалять,
// регистрирует bootstrap; запросы остаются у владельцев таблиц.
package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Kind — вид задания (07 §2: раз в сутки, 1 попытка).
const Kind = "cleanup"

// batchSize — строк за один DELETE: без долгих блокировок.
const batchSize = 1000

// maxBatches — предел пачек одной таблицы за запуск; остаток удалит следующий запуск.
const maxBatches = 1000

// DeleteFunc удаляет до batch строк старше before и возвращает число удалённых.
type DeleteFunc func(ctx context.Context, before time.Time, batch int32) (int64, error)

// Task — что удалять.
type Task struct {
	// Name — таблица и условие для журнала: outbound_request, job_succeeded…
	Name      string
	Retention time.Duration
	Delete    DeleteFunc
}

// Cleaner выполняет зарегистрированные задачи очистки.
type Cleaner struct {
	clock clock.Clock
	log   *slog.Logger
	tasks []Task
}

// New создаёт Cleaner без задач.
func New(clk clock.Clock, log *slog.Logger) *Cleaner { return &Cleaner{clock: clk, log: log} }

// Add регистрирует задачу.
func (c *Cleaner) Add(t Task) { c.tasks = append(c.tasks, t) }

// Register регистрирует периодическое задание: раз в сутки в 04:00 по Москве.
func (c *Cleaner) Register(w *job.Worker) {
	w.RegisterPeriodic(Kind, job.DailyAt(4, 0), c.Run, job.HandlerOptions{})
}

// Run удаляет устаревшие строки по всем задачам; ошибка одной не останавливает остальные.
func (c *Cleaner) Run(ctx context.Context, _ job.Running) error {
	var failed []string
	for _, t := range c.tasks {
		if err := c.run(ctx, t); err != nil {
			c.log.ErrorContext(ctx, "cleanup failed", logx.KeyOp, "Cleanup.Run", "table", t.Name, logx.KeyError, err.Error())
			failed = append(failed, t.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("cleanup failed for %v", failed)
	}
	return nil
}

func (c *Cleaner) run(ctx context.Context, t Task) error {
	started := c.clock.Now()
	before := started.Add(-t.Retention)
	var total int64
	for range maxBatches {
		n, err := t.Delete(ctx, before, batchSize)
		if err != nil {
			return err
		}
		total += n
		if n < batchSize {
			break
		}
	}
	c.log.InfoContext(ctx, "cleanup finished", logx.KeyOp, "Cleanup.Run", "table", t.Name, "deleted", total,
		"retention", t.Retention.String(), "duration_ms", c.clock.Now().Sub(started).Milliseconds())
	return nil
}
