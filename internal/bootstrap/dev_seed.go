package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// modeDevSeed — инфраструктура maxim dev seed: хранилище (документы, изображения) и Рокет Ворк.
// MAX, ОРД и почта не подключаются: шаги с ними выполняет запущенный worker (R-3 плана dev seed).
const modeDevSeed = "dev-seed"

// Причины пропуска сценария (журнал и сводка).
const (
	skipExists           = "exists"
	skipRocketWorkNoFake = "rocketwork_not_fake"
)

// errWorkerTimeout — worker не довёл шаг за --wait.
var errWorkerTimeout = errors.New("is the worker running? (docker compose -f compose.dev.yml up worker)")

// seedWaiter ждёт, пока worker доведёт шаг: done проверяет состояние в базе (R-4).
type seedWaiter interface {
	Wait(ctx context.Context, what string, done func(ctx context.Context) (bool, error)) error
}

// pollWaiter опрашивает базу раз в every не дольше limit.
type pollWaiter struct {
	every, limit time.Duration
	clk          clock.Clock
	log          *slog.Logger
}

func (w pollWaiter) Wait(ctx context.Context, what string, done func(ctx context.Context) (bool, error)) error {
	const op = "Bootstrap.DevSeed"
	start := w.clk.Now()
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
		elapsed := w.clk.Now().Sub(start)
		w.log.DebugContext(ctx, "seed waiting", logx.KeyOp, op, "what", what, "elapsed_ms", elapsed.Milliseconds())
		if elapsed >= w.limit {
			return fmt.Errorf("worker did not %s within %s: %w", what, w.limit, errWorkerTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// runDevSeed — тестовые данные разработки (план dev seed):
//
//	maxim dev seed [--wait 5m] [--orders=true]
//
// Создаёт через сервисы приложения пользователей, организации, каналы каталога, кампании и
// заказы в разных состояниях. Шаги с MAX и ОРД выполняет запущенный worker, команда ждёт их
// результата в базе не дольше --wait. Повторный запуск находит созданное и ничего не дублирует.
func runDevSeed(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.DevSeed"
	fs := flag.NewFlagSet("dev seed", flag.ContinueOnError)
	fs.SetOutput(out)
	wait := fs.Duration("wait", 5*time.Minute, "how long to wait for the worker on each step")
	orders := fs.Bool("orders", true, "create orders and placements (needs the worker)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *wait <= 0 {
		return errUsage
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, modeDevSeed, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	if err := m.startMarking(ctx); err != nil {
		return err
	}
	log.WarnContext(ctx, "dev command used", logx.KeyOp, op, "command", "seed", "orders", *orders, "wait", wait.String())
	s := &seeder{cfg: cfg, m: m, log: log, out: out, orders: *orders,
		wait: pollWaiter{every: 2 * time.Second, limit: *wait, clk: in.clock, log: log}}
	// Состояние подделки Рокет Ворк должно быть общим с api и worker, иначе worker не найдёт исполнителя.
	if fake, ok := in.rocketwork.(*rocketworkfake.Fake); ok && cfg.RocketWork.FakeStatePath != "" {
		s.rw = fake
	}
	return s.run(ctx)
}

// seeder — этапы dev seed над собранными модулями. Тест подставляет модули тестового
// приложения и ожидание, которое само прогоняет worker.
type seeder struct {
	cfg    Config
	m      *modules
	log    *slog.Logger
	out    io.Writer
	wait   seedWaiter
	orders bool
	// rw — подделка Рокет Ворк с общим состоянием; nil — сценарии с исполнителем пропускаются.
	rw *rocketworkfake.Fake

	admin, moderator uuid.UUID
	adv, blg, blgUL  seedOrg
	// channels — активные каналы каталога по порядку: самозанятого, затем юрлица.
	channels []seedChannel
	created  []string
	skipped  []string
}

// seedOrg — пользователь и его организация.
type seedOrg struct {
	email  string
	userID uuid.UUID
	orgID  uuid.UUID
}

// seedChannel — канал каталога и его владелец.
type seedChannel struct {
	id    uuid.UUID
	title string
	owner *seedOrg
}

type seedStage struct {
	name string
	fn   func(ctx context.Context) error
}

func (s *seeder) run(ctx context.Context) error {
	const op = "Bootstrap.DevSeed"
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})
	stages := []seedStage{
		{"legal", s.seedLegal},
		{"people", s.seedPeople},
		{"money", s.seedMoney},
		{"channels", s.seedChannels},
		{"campaigns", s.seedCampaigns},
	}
	if s.orders {
		stages = append(stages, seedStage{"orders", s.seedOrders})
	}
	stages = append(stages, seedStage{"check", s.checkLedger})
	for _, st := range stages {
		start := s.m.in.clock.Now()
		created, skipped := len(s.created), len(s.skipped)
		s.log.InfoContext(ctx, "seed stage started", logx.KeyOp, op, "stage", st.name)
		if err := st.fn(ctx); err != nil {
			s.log.ErrorContext(ctx, "seed failed", logx.KeyOp, op, "stage", st.name, logx.KeyError, err.Error())
			s.summary()
			return fmt.Errorf("seed %s: %w", st.name, err)
		}
		s.log.InfoContext(ctx, "seed stage done", logx.KeyOp, op, "stage", st.name, "created", len(s.created)-created,
			"skipped", len(s.skipped)-skipped, "duration_ms", s.m.in.clock.Now().Sub(start).Milliseconds())
	}
	s.summary()
	return nil
}

func (s *seeder) note(format string, args ...any) {
	s.created = append(s.created, fmt.Sprintf(format, args...))
}

func (s *seeder) skip(ctx context.Context, scenario, reason string) {
	s.log.WarnContext(ctx, "seed scenario skipped", logx.KeyOp, "Bootstrap.DevSeed", "scenario", scenario, "reason", reason)
	s.skipped = append(s.skipped, scenario+": "+reason)
}

// summary печатает итог: учётки для входа, что создано и что пропущено.
func (s *seeder) summary() {
	fmt.Fprintln(s.out, "Вход — по коду из письма (Mailpit):")
	for _, l := range [][2]string{
		{seedAdvEmail, "рекламодатель, ООО"}, {seedBlgEmail, "блогер, самозанятый"}, {seedBlgULEmail, "блогер, юрлицо"},
		{seedAdminEmail, "администратор senior"}, {seedModeratorEmail, "модератор"},
	} {
		fmt.Fprintf(s.out, "  %-24s %s\n", l[0], l[1])
	}
	fmt.Fprintf(s.out, "Создано: %d\n", len(s.created))
	for _, c := range s.created {
		fmt.Fprintln(s.out, "  "+c)
	}
	if len(s.skipped) > 0 {
		fmt.Fprintf(s.out, "Пропущено: %d\n", len(s.skipped))
		for _, c := range s.skipped {
			fmt.Fprintln(s.out, "  "+c)
		}
	}
	if s.orders {
		fmt.Fprintln(s.out, "Состояния идут по настоящим часам: scheduled публикуется примерно через час, "+
			"published снимается по сроку формата, неотвеченные заявки истекают через сутки.")
	}
}

// checkLedger — журнал проводок сходится после всех этапов.
func (s *seeder) checkLedger(ctx context.Context) error {
	rep, err := s.m.ledger.CheckBalance(ctx)
	if err != nil {
		return err
	}
	if !rep.OK() {
		return fmt.Errorf("ledger check failed: %+v", rep)
	}
	return nil
}
