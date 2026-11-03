package job

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Параметры правил после простоя (07 §2, R-6).
const (
	heartbeatInterval     = 30 * time.Second
	defaultDowntimeAfter  = 30 * time.Minute
	maxReportedObjects    = 50
	reasonDowntime        = "downtime"
	reasonJobFailedPrefix = "job_failed:"
)

// Alerter поднимает оповещение администратору (07 §3). Реализация —
// platform/alert; пакет job от неё не зависит.
type Alerter interface {
	RaiseError(ctx context.Context, reason, objectKey, summary string, fields map[string]any)
}

type nopAlerter struct{}

func (nopAlerter) RaiseError(context.Context, string, string, string, map[string]any) {}

// Overdue — политика задания, взятого в работу позже назначенного (07 §2).
// Нулевое значение — выполнить немедленно (remove_placement, placement_timer).
type Overdue struct {
	// After — допустимая просрочка; больше — вместо обработчика вызывается OnExpired.
	// Пример: publish_placement, After = 30 мин (T-03), OnExpired — P-09.
	After time.Duration
	// OnExpired обрабатывает просроченное задание; ошибка — обычный повтор.
	OnExpired Handler
}

// SetAlerter подключает оповещения: исчерпание попыток, простой.
func (w *Worker) SetAlerter(a Alerter) {
	if a == nil {
		a = nopAlerter{}
	}
	w.alerter = a
}

// SetDowntimeThreshold задаёт, какой разрыв в отметках считается простоем.
func (w *Worker) SetDowntimeThreshold(d time.Duration) {
	if d > 0 {
		w.downtimeAfter = d
	}
}

// Heartbeat отмечает экземпляр в worker_heartbeat.
func (w *Worker) Heartbeat(ctx context.Context) error {
	err := sqlc.New(w.pool).UpsertHeartbeat(ctx, sqlc.UpsertHeartbeatParams{Instance: w.name, Now: w.clock.Now()})
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	w.log.DebugContext(ctx, "heartbeat", logx.KeyOp, "Job.Heartbeat", "locked_by", w.name)
	return nil
}

// CheckDowntime сравнивает последнюю отметку любого экземпляра с текущим
// временем, затем отмечает себя. Разрыв больше порога — ERROR со списком
// затронутых объектов и оповещение (07 §2). Возвращает длительность простоя или 0.
func (w *Worker) CheckDowntime(ctx context.Context) (time.Duration, error) {
	q := sqlc.New(w.pool)
	last, err := q.LastHeartbeat(ctx)
	noHistory := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !noHistory {
		return 0, fmt.Errorf("last heartbeat: %w", err)
	}
	if err := w.Heartbeat(ctx); err != nil {
		return 0, err
	}
	now := w.clock.Now()
	if noHistory {
		w.log.InfoContext(ctx, "no previous worker heartbeat", logx.KeyOp, "Job.CheckDowntime")
		return 0, nil
	}
	gap := now.Sub(last)
	if gap <= w.downtimeAfter {
		w.log.DebugContext(ctx, "no downtime detected", logx.KeyOp, "Job.CheckDowntime", "gap", gap.String())
		return 0, nil
	}

	counts, err := q.OverdueJobCounts(ctx, now)
	if err != nil {
		return gap, fmt.Errorf("overdue job counts: %w", err)
	}
	overdue := map[string]int64{}
	for _, c := range counts {
		overdue[c.Kind] = c.Count
	}
	objects, err := w.affectedObjects(ctx, now)
	if err != nil {
		return gap, err
	}
	w.log.ErrorContext(ctx, "worker started after downtime", logx.KeyOp, "Job.CheckDowntime",
		"downtime", gap.Round(time.Second).String(), "last_seen_at", last, "overdue_jobs", overdue, "affected_objects", objects)
	w.alerter.RaiseError(ctx, reasonDowntime, "",
		fmt.Sprintf("Worker was down for %s", gap.Round(time.Second)),
		map[string]any{"overdue_jobs": overdue, "affected_objects": objects})
	return gap, nil
}

// affectedObjects — объекты просроченных заданий высокого приоритета: для
// размещений — их идентификаторы (07 §2, «список затронутых размещений»).
func (w *Worker) affectedObjects(ctx context.Context, now time.Time) ([]string, error) {
	var kinds []string
	for kind, reg := range w.handlers {
		if reg.opts.Priority == PriorityHigh && reg.opts.ObjectRef != nil {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return []string{}, nil
	}
	rows, err := sqlc.New(w.pool).OverdueJobs(ctx, sqlc.OverdueJobsParams{RunBefore: now, Kinds: kinds})
	if err != nil {
		return nil, fmt.Errorf("overdue jobs: %w", err)
	}
	objects := []string{}
	for _, r := range rows {
		payload, _ := decodePayload(r.Payload)
		if ref := w.handlers[r.Kind].opts.ObjectRef(payload); ref != "" && !slices.Contains(objects, ref) {
			objects = append(objects, ref)
		}
		if len(objects) == maxReportedObjects {
			break
		}
	}
	return objects, nil
}

// expired сообщает, что задание просрочено больше допустимого по политике вида.
func expired(opts HandlerOptions, runAt, now time.Time) bool {
	return opts.Overdue.After > 0 && opts.Overdue.OnExpired != nil && now.Sub(runAt) > opts.Overdue.After
}
