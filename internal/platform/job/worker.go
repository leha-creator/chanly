package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Параметры обработчика по 07 §2.
const (
	retryBase        = 30 * time.Second
	retryMax         = time.Hour
	abandonedAfter   = 10 * time.Minute
	defaultBatch     = 8
	pollInterval     = time.Second
	abandonInterval  = 30 * time.Second
	jobTimeout       = 5 * time.Minute // меньше порога брошенного задания
	shutdownDeadline = 30 * time.Second
)

// Running — задание, взятое в работу.
type Running struct {
	ID       uuid.UUID
	Kind     string
	Payload  []byte // JSON без служебных полей
	Attempts int    // сколько раз задание уже завершалось ошибкой
}

// Handler выполняет задание. Ошибка — повтор с задержкой или failed.
type Handler func(ctx context.Context, j Running) error

// HandlerOptions — особенности вида задания.
type HandlerOptions struct {
	// WipePayload затирает полезную нагрузку после завершения (успех или
	// исчерпание попыток): для заданий с секретами (R-1).
	WipePayload bool
	// Priority — высокий приоритет у публикации, снятия и таймеров размещения:
	// такие задания берутся первыми и получают резерв мест (R-8).
	Priority Priority
	// Overdue — что делать с заданием, взятым позже срока (07 §2, «После простоя»).
	Overdue Overdue
	// ObjectRef извлекает из полезной нагрузки объект задания для списка
	// затронутых объектов после простоя (например, placement:<id>).
	ObjectRef func(payload []byte) string
	// OnExhausted вызывается после перевода задания в failed: отметить связанный
	// объект (например, inbound_event.status = failed). err — последняя ошибка.
	OnExhausted func(ctx context.Context, j Running, err error)
	// NoFailureAlert — не поднимать оповещение при исчерпании попыток: для самого
	// задания отправки оповещений, чтобы сбой почты не порождал новые оповещения.
	NoFailureAlert bool
}

// Priority — приоритет вида задания при взятии в работу.
type Priority int

const (
	PriorityNormal Priority = iota
	PriorityHigh
)

// reservedHigh — места пакета, которые задания обычного приоритета не занимают,
// если зарегистрирован хотя бы один вид высокого приоритета (R-8).
const reservedHigh = 2

type registration struct {
	handler Handler
	opts    HandlerOptions
	// schedule задан у периодических заданий.
	schedule Schedule
}

// Worker берёт задания из таблицы и выполняет их обработчиками по виду.
type Worker struct {
	pool     *pgxpool.Pool
	clock    clock.Clock
	log      *slog.Logger
	name     string
	handlers map[string]registration
	batch    int
	queue    *Queue

	alerter       Alerter
	downtimeAfter time.Duration

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
	wg      sync.WaitGroup
}

// NewWorker создаёт обработчик. Имя экземпляра — хост и PID (locked_by).
func NewWorker(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Worker {
	host, _ := os.Hostname()
	return &Worker{
		pool:     pool,
		clock:    clk,
		log:      log,
		name:     fmt.Sprintf("%s:%d", host, os.Getpid()),
		handlers: map[string]registration{},
		batch:    defaultBatch,
		running:  map[uuid.UUID]context.CancelFunc{},
		queue:    NewQueue(pool, clk, log),

		alerter:       nopAlerter{},
		downtimeAfter: defaultDowntimeAfter,
	}
}

// RegisterPeriodic регистрирует периодическое задание: одна попытка, запуск по
// schedule. Пропущенные интервалы не догоняются: после простоя — один запуск (07 §2).
func (w *Worker) RegisterPeriodic(kind string, schedule Schedule, h Handler, opts HandlerOptions) {
	w.handlers[kind] = registration{handler: h, opts: opts, schedule: schedule}
}

// PeriodicKey — dedupe_key живого задания периодического вида.
func PeriodicKey(kind string) string { return "periodic:" + kind }

// EnsurePeriodic создаёт задания периодических видов, у которых нет живого
// задания. Гонку экземпляров решает dedupe_key.
func (w *Worker) EnsurePeriodic(ctx context.Context) error {
	for _, kind := range w.Kinds() {
		reg := w.handlers[kind]
		if reg.schedule == nil {
			continue
		}
		if err := w.enqueuePeriodic(ctx, kind, reg.schedule); err != nil {
			return err
		}
		w.log.InfoContext(ctx, "periodic job registered", logx.KeyOp, "Job.EnsurePeriodic", "kind", kind,
			"schedule", reg.schedule.String())
	}
	return nil
}

func (w *Worker) enqueuePeriodic(ctx context.Context, kind string, s Schedule) error {
	runAt := s.Next(w.clock.Now())
	if err := w.queue.Enqueue(ctx, Job{Kind: kind, DedupeKey: PeriodicKey(kind), RunAt: runAt, MaxAttempts: 1}); err != nil {
		return err
	}
	w.log.DebugContext(ctx, "periodic job scheduled", logx.KeyOp, "Job.SchedulePeriodic", "kind", kind, "run_at", runAt)
	return nil
}

func (w *Worker) schedulePeriodic(ctx context.Context, kind string, s Schedule) {
	if err := w.enqueuePeriodic(ctx, kind, s); err != nil {
		w.log.ErrorContext(ctx, "periodic job schedule failed", logx.KeyOp, "Job.SchedulePeriodic", "kind", kind,
			logx.KeyError, err.Error())
	}
}

// Register связывает вид задания с обработчиком.
func (w *Worker) Register(kind string, h Handler, opts HandlerOptions) {
	w.handlers[kind] = registration{handler: h, opts: opts}
}

// Kinds — зарегистрированные виды.
func (w *Worker) Kinds() []string {
	return slices.Sorted(maps.Keys(w.handlers))
}

// kindsByPriority делит зарегистрированные виды на высокий и обычный приоритет.
func (w *Worker) kindsByPriority() (high, normal []string) {
	for _, k := range w.Kinds() {
		if w.handlers[k].opts.Priority == PriorityHigh {
			high = append(high, k)
		} else {
			normal = append(normal, k)
		}
	}
	return high, normal
}

// Run обрабатывает задания, пока не отменён ctx. После отмены новые задания не
// берутся, текущие доделываются до shutdownDeadline, остальные возвращаются в очередь.
func (w *Worker) Run(ctx context.Context) error {
	w.log.InfoContext(ctx, "worker started", logx.KeyOp, "Job.Run", "locked_by", w.name, "kinds", w.Kinds())
	if _, err := w.CheckDowntime(ctx); err != nil {
		return fmt.Errorf("check downtime: %w", err)
	}
	w.releaseAbandoned(ctx)
	if err := w.EnsurePeriodic(ctx); err != nil {
		return fmt.Errorf("ensure periodic jobs: %w", err)
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	abandon := time.NewTicker(abandonInterval)
	defer abandon.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	jobsCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	for {
		select {
		case <-ctx.Done():
			w.shutdown(cancelJobs)
			return nil
		case <-abandon.C:
			w.releaseAbandoned(ctx)
		case <-heartbeat.C:
			if err := w.Heartbeat(ctx); err != nil && ctx.Err() == nil {
				w.log.ErrorContext(ctx, "heartbeat failed", logx.KeyOp, "Job.Run", logx.KeyError, err.Error())
			}
		case <-poll.C:
			if _, err := w.claimAndStart(jobsCtx); err != nil && ctx.Err() == nil {
				w.log.ErrorContext(ctx, "job poll failed", logx.KeyOp, "Job.Run", logx.KeyError, err.Error())
			}
		}
	}
}

// RunOnce берёт готовые задания и выполняет их синхронно. Возвращает число
// выполненных. Нужен тестам: время задаётся clock.Manual.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	n, err := w.claimAndStart(ctx)
	w.wg.Wait()
	return n, err
}

// ReleaseAbandoned возвращает в очередь брошенные задания (07 §2).
func (w *Worker) ReleaseAbandoned(ctx context.Context) int { return w.releaseAbandoned(ctx) }

func (w *Worker) claimAndStart(ctx context.Context) (int, error) {
	if len(w.handlers) == 0 {
		return 0, nil
	}
	high, normal := w.kindsByPriority()
	n := 0
	if len(high) > 0 {
		started, err := w.claim(ctx, high, w.batch)
		n += started
		if err != nil {
			return n, err
		}
	}
	if len(normal) > 0 {
		limit := w.batch
		if len(high) > 0 {
			limit -= reservedHigh
		}
		started, err := w.claim(ctx, normal, limit)
		n += started
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// claim берёт готовые задания видов kinds, пока занятых мест меньше limit, и запускает их.
func (w *Worker) claim(ctx context.Context, kinds []string, limit int) (int, error) {
	w.mu.Lock()
	free := limit - len(w.running)
	w.mu.Unlock()
	if free <= 0 {
		return 0, nil
	}
	now := w.clock.Now()
	rows, err := sqlc.New(w.pool).ClaimJobs(ctx, sqlc.ClaimJobsParams{
		LockedBy: &w.name, Now: now, Kinds: kinds, Batch: int32(free),
	})
	if err != nil {
		return 0, fmt.Errorf("claim jobs: %w", err)
	}
	for _, r := range rows {
		jctx, cancel := context.WithTimeout(ctx, jobTimeout)
		w.mu.Lock()
		w.running[r.ID] = cancel
		w.mu.Unlock()
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			defer w.finish(r.ID, cancel)
			w.execute(jctx, r)
		}()
	}
	return len(rows), nil
}

func (w *Worker) finish(id uuid.UUID, cancel context.CancelFunc) {
	cancel()
	w.mu.Lock()
	delete(w.running, id)
	w.mu.Unlock()
}

func (w *Worker) execute(ctx context.Context, r sqlc.ClaimJobsRow) {
	payload, requestID := decodePayload(r.Payload)
	if requestID != "" {
		ctx = logx.WithRequestID(ctx, requestID)
	}
	ctx = logx.With(ctx, logx.KeyJobID, r.ID.String())
	reg := w.handlers[r.Kind]
	attempt := int(r.Attempts) + 1
	start := w.clock.Now()
	w.log.InfoContext(ctx, "job started", logx.KeyOp, "Job.Execute", "kind", r.Kind, "attempt", attempt)

	handler := reg.handler
	if expired(reg.opts, r.RunAt, start) {
		w.log.WarnContext(ctx, "overdue job expired", logx.KeyOp, "Job.Execute", "kind", r.Kind,
			"overdue", start.Sub(r.RunAt).Round(time.Second).String(), "allowed", reg.opts.Overdue.After.String())
		handler = reg.opts.Overdue.OnExpired
	}
	err := safeCall(ctx, handler, Running{ID: r.ID, Kind: r.Kind, Payload: payload, Attempts: int(r.Attempts)})
	// Итог пишется и после отмены context задания: до остановки процесса.
	dbctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if reg.schedule != nil {
		// 07 §2: следующий запуск создаётся независимо от исхода, от фактического времени.
		defer w.schedulePeriodic(dbctx, r.Kind, reg.schedule)
	}
	q := sqlc.New(w.pool)
	now := w.clock.Now()
	dur := now.Sub(start).Milliseconds()

	if err == nil {
		if _, uerr := q.CompleteJob(dbctx, sqlc.CompleteJobParams{
			ID: r.ID, LockedBy: &w.name, Now: now, WipePayload: reg.opts.WipePayload,
		}); uerr != nil {
			w.log.ErrorContext(ctx, "job state update failed", logx.KeyOp, "Job.Execute", logx.KeyError, uerr.Error())
			return
		}
		w.log.InfoContext(ctx, "job finished", logx.KeyOp, "Job.Execute", "kind", r.Kind, "attempt", attempt, "duration_ms", dur)
		return
	}

	msg := err.Error()
	if after, ok := PostponedFor(err); ok {
		next := now.Add(after)
		if _, uerr := q.RetryJob(dbctx, sqlc.RetryJobParams{
			ID: r.ID, LockedBy: &w.name, Now: now, Attempts: r.Attempts, RunAt: next, LastError: &msg,
		}); uerr != nil {
			w.log.ErrorContext(ctx, "job state update failed", logx.KeyOp, "Job.Execute", logx.KeyError, uerr.Error())
			return
		}
		w.log.DebugContext(ctx, "job postponed", logx.KeyOp, "Job.Execute", "kind", r.Kind, "attempt", attempt,
			"next_run_at", next, "duration_ms", dur, "reason", msg)
		return
	}
	attempts := r.Attempts + 1
	permanent := IsPermanent(err)
	if permanent || attempts >= r.MaxAttempts {
		if _, uerr := q.FailJob(dbctx, sqlc.FailJobParams{
			ID: r.ID, LockedBy: &w.name, Now: now, Attempts: attempts, LastError: &msg, WipePayload: reg.opts.WipePayload,
		}); uerr != nil {
			w.log.ErrorContext(ctx, "job state update failed", logx.KeyOp, "Job.Execute", logx.KeyError, uerr.Error())
			return
		}
		if permanent {
			w.log.ErrorContext(ctx, "job failed permanently", logx.KeyOp, "Job.Execute", "kind", r.Kind,
				"attempt", attempt, "duration_ms", dur, logx.KeyError, msg)
		} else {
			w.log.ErrorContext(ctx, "job exhausted attempts", logx.KeyOp, "Job.Execute", "kind", r.Kind,
				"attempt", attempt, "max_attempts", r.MaxAttempts, "duration_ms", dur, logx.KeyError, msg)
		}
		if reg.opts.OnExhausted != nil {
			reg.opts.OnExhausted(dbctx, Running{ID: r.ID, Kind: r.Kind, Payload: payload, Attempts: int(attempts)}, err)
		}
		// 07 §2: после max_attempts — failed и оповещение.
		if !reg.opts.NoFailureAlert {
			w.alerter.RaiseError(dbctx, reasonJobFailedPrefix+r.Kind, r.ID.String(),
				fmt.Sprintf("Job %s failed after %d attempts", r.Kind, attempts),
				map[string]any{"job_id": r.ID.String(), "kind": r.Kind, "attempts": attempts, "permanent": permanent})
		}
		return
	}
	delay, reason := RetryDelay(int(r.Attempts)), "backoff"
	if after, ok := RetryAfter(err); ok {
		delay, reason = after, "requested"
	}
	next := now.Add(delay)
	if _, uerr := q.RetryJob(dbctx, sqlc.RetryJobParams{
		ID: r.ID, LockedBy: &w.name, Now: now, Attempts: attempts, RunAt: next, LastError: &msg,
	}); uerr != nil {
		w.log.ErrorContext(ctx, "job state update failed", logx.KeyOp, "Job.Execute", logx.KeyError, uerr.Error())
		return
	}
	w.log.WarnContext(ctx, "job failed, will retry", logx.KeyOp, "Job.Execute", "kind", r.Kind,
		"attempt", attempt, "next_run_at", next, "retry_reason", reason, "duration_ms", dur, logx.KeyError, msg)
}

// RetryDelay — задержка перед повтором: min(30 с × 2^attempts, 1 ч), где
// attempts — число неудач до этой (07 §2).
func RetryDelay(attempts int) time.Duration {
	if attempts >= 7 { // 30 с × 2^7 больше часа
		return retryMax
	}
	return min(retryBase<<attempts, retryMax)
}

func safeCall(ctx context.Context, h Handler, r Running) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("job handler panic: %v", p)
		}
	}()
	return h(ctx, r)
}

func (w *Worker) releaseAbandoned(ctx context.Context) int {
	now := w.clock.Now()
	rows, err := sqlc.New(w.pool).ReleaseAbandonedJobs(ctx, sqlc.ReleaseAbandonedJobsParams{
		Now: now, LockedBefore: now.Add(-abandonedAfter),
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.log.ErrorContext(ctx, "abandoned jobs check failed", logx.KeyOp, "Job.ReleaseAbandoned", logx.KeyError, err.Error())
		}
		return 0
	}
	for _, r := range rows {
		w.log.WarnContext(ctx, "abandoned job returned to queue", logx.KeyOp, "Job.ReleaseAbandoned",
			logx.KeyJobID, r.ID, "kind", r.Kind)
	}
	return len(rows)
}

// shutdown ждёт текущие задания до shutdownDeadline, затем отменяет их и
// возвращает в очередь.
func (w *Worker) shutdown(cancelJobs context.CancelFunc) {
	ctx := context.Background()
	w.log.InfoContext(ctx, "worker stopping", logx.KeyOp, "Job.Shutdown")
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		w.log.InfoContext(ctx, "worker stopped", logx.KeyOp, "Job.Shutdown")
		return
	case <-time.After(shutdownDeadline):
	}
	w.mu.Lock()
	ids := slices.Collect(maps.Keys(w.running))
	w.mu.Unlock()
	cancelJobs()
	<-done
	n, err := sqlc.New(w.pool).ReleaseJobs(ctx, sqlc.ReleaseJobsParams{Ids: ids, LockedBy: &w.name, Now: w.clock.Now()})
	if err != nil {
		w.log.ErrorContext(ctx, "jobs release failed", logx.KeyOp, "Job.Shutdown", logx.KeyError, err.Error())
		return
	}
	w.log.WarnContext(ctx, "unfinished jobs returned to queue", logx.KeyOp, "Job.Shutdown", "count", n)
}
