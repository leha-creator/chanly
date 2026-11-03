// Package dbtest запускает настоящую PostgreSQL для тестов (07 §9): один
// контейнер на тестовый пакет, миграции от имени владельца схемы, а тесты
// работают под ролью приложения — так проверяются и права (04 §1).
//
// Использование:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
//	func TestX(t *testing.T) { d := dbtest.New(t); ... d.Pool ... }
//
// Без Docker тесты с базой пропускаются; в CI (CI=true) — падают.
package dbtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Image — образ PostgreSQL для тестов.
const Image = "postgres:17-alpine"

// AppRole — роль приложения в тестовой базе.
const AppRole = "maxim_app"

// DB — тестовая база.
type DB struct {
	// Pool — соединения под ролью приложения.
	Pool *pgxpool.Pool
	// Owner — соединения владельца схемы: очистка, проверки прав.
	Owner *pgxpool.Pool
	// OwnerURL — строка подключения владельца (для тестов миграций).
	OwnerURL string
	// AppURL — строка подключения роли приложения.
	AppURL string
}

var (
	mu       sync.Mutex
	shared   *DB
	startErr error
	cleanup  func()
)

// Main запускает тесты пакета и останавливает контейнер после них.
func Main(m *testing.M) int {
	code := m.Run()
	mu.Lock()
	defer mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
	return code
}

// New возвращает базу пакета с пустыми таблицами. Контейнер запускается при
// первом вызове.
func New(t testing.TB) *DB {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if shared == nil && startErr == nil {
		shared, cleanup, startErr = start()
	}
	if startErr != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("dbtest: PostgreSQL is required in CI: %v", startErr)
		}
		t.Skipf("dbtest: Docker is not available, database tests are skipped: %v", startErr)
	}
	if err := shared.Truncate(context.Background()); err != nil {
		t.Fatalf("dbtest: truncate: %v", err)
	}
	return shared
}

func start() (*DB, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("maxim"),
		postgres.WithUsername("maxim"),
		postgres.WithPassword("maxim"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("start postgres: %w", err)
	}
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	fail := func(err error) (*DB, func(), error) {
		stop()
		return nil, nil, err
	}

	ownerURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fail(err)
	}
	log := logx.Discard()
	owner, err := db.Open(ctx, ownerURL, 4, log)
	if err != nil {
		return fail(err)
	}
	if _, err := owner.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", AppRole, AppRole)); err != nil {
		return fail(fmt.Errorf("create app role: %w", err))
	}
	if err := db.MigrateURL(ctx, ownerURL, AppRole, db.MigrateUp, log); err != nil {
		return fail(err)
	}
	appURL := strings.Replace(ownerURL, "maxim:maxim@", AppRole+":"+AppRole+"@", 1)
	app, err := db.Open(ctx, appURL, 10, log)
	if err != nil {
		return fail(err)
	}
	d := &DB{Pool: app, Owner: owner, OwnerURL: ownerURL, AppURL: appURL}
	return d, func() {
		app.Close()
		owner.Close()
		stop()
	}, nil
}

// referenceTables — справочники, которые заполняют миграции; очистка их не трогает.
var referenceTables = []string{"goose_db_version", "channel_category", "placement_format"}

// Truncate очищает все таблицы приложения, кроме справочников из миграций, и сбрасывает
// их последовательности (номер заказа снова начинается с 1001).
func (d *DB) Truncate(ctx context.Context) error {
	rows, err := d.Owner.Query(ctx,
		`SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname = 'public' AND NOT (tablename = ANY($1))`, referenceTables)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	_, err = d.Owner.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE")
	return err
}
