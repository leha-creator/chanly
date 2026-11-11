// Command maxim — бэкенд биржи рекламы в каналах MAX.
//
// Режимы:
//
//	maxim api                       — HTTP API кабинетов
//	maxim worker                    — обработчик заданий
//	maxim migrate up|down|status    — миграции базы
//	maxim legal publish|list        — оферты и правила (до API администратора, R-8)
//	maxim admin grant|revoke        — роль администратора (до API администратора)
//	maxim admin ord errors|retry|report — ошибки Яндекс ОРД, повтор, ежемесячный отчёт
//	maxim admin pd export|anonymize — запросы субъекта персональных данных
//	maxim dev post-version          — двинуть версию поста заказа (только development и test, R-23)
//	maxim dev seed                  — тестовые данные разработки (только development и test)
//	maxim config check --for <mode>  — проверить конфигурацию режима до выпуска
//	maxim probe <url>                — проверка здоровья для HEALTHCHECK (код 0 при 200)
//
// Конфигурация — переменные окружения, см. .env.example.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/leha-creator/chanly/internal/bootstrap"
)

// version задаётся при сборке: -ldflags "-X main.version=<sha>".
var version = "dev"

const usage = `usage: maxim <command> [args]

commands:
  rocketwork-check         fingerprint|configure-account|issue|revoke|reauthorize|kill|arm|prepare|inspect|execute|resume|reconcile|status|import
  api                      run HTTP API
  worker                   run job worker
  migrate up|down|status   apply, roll back one or show migrations
  legal publish|list       publish a legal document version or list current ones
  admin grant|revoke       grant (--email, --role moderator|senior) or revoke (--email) an admin role
  admin ord errors         list ORD objects in error [--limit N]
  admin ord retry          --object <type>:<id> --reason <text> --email <admin>: retry an ORD object
  admin ord report         show|build|send --period YYYY-MM [--reason <text> --email <senior>]: monthly ORD report
  admin pd export          --email <subject> --operator-email <senior>: personal data of a subject as JSON
  admin pd anonymize       --email <subject> --reason <text> --operator-email <senior>: anonymize a subject
  admin money              statement import | bank-tx list|match|ignore | invoice cancel | ledger manual|list |
                           balance-return | reconcile | closing-acts: money operations (senior admin, --email)
  dev post-version         --order <number> approve|ready|reject [--erid X] [--reason Y] (development and test only;
                           ready only with ORD_ENABLED=false)
  dev balance deposit      --email <user> --amount <rub.kop>: invoice and paying statement (development and test only)
  dev statement            --invoice MX-… [--amount <rub.kop>] [--out <path>]: statement file paying an invoice
  dev seed                 [--wait 5m] [--orders=true]: demo users, catalog, campaigns and orders; needs a running
                           worker (development and test only)
  dev channel-metrics      --channel <UUID>: queue an immediate metrics refresh; needs a running worker
                           (development and test only; updates today's snapshot)
  config check --for <mode>  validate configuration of a mode (api, worker, migrate, legal, admin, dev) and exit
  integration-check        --system max|ord|rocketwork|tbank [--scenario preflight|inspect|sandbox|sync|publish|observe|recover|remove]
                           [--chat ID] [--run-dir PATH] [--execute]: isolated integration checks (development only)
  probe <url>              GET the url with a 2 s timeout: exit 0 on 200 (container health check)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := bootstrap.Run(ctx, bootstrap.RunParams{
		Command: os.Args[1],
		Args:    os.Args[2:],
		Environ: os.Environ(),
		Version: version,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	})
	stop()
	if code == 2 {
		fmt.Fprint(os.Stderr, usage)
	}
	os.Exit(code)
}
