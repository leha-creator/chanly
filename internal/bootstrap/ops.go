package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync/atomic"
	"time"

	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Служебные режимы эксплуатации (deploy/app/README.md).
const (
	// ModeProbe — проверка здоровья для HEALTHCHECK compose: в distroless нет оболочки и curl.
	ModeProbe = "probe"
	// ModeConfig — проверка конфигурации режима до выпуска (maxim config check --for api).
	ModeConfig = "config"
)

const probeTimeout = 2 * time.Second

// configCheckModes — режимы, конфигурацию которых проверяет maxim config check.
var configCheckModes = []string{ModeAPI, ModeWorker, ModeMigrate, ModeLegal, ModeAdmin, ModeDev}

// errStopping — процесс останавливается: /readyz отвечает 503, прокси убирает экземпляр.
var errStopping = errors.New("stopping")

// lifecycle — признак остановки процесса для /readyz (07 §8, выпуск без простоя).
type lifecycle struct{ stopping atomic.Bool }

// ready добавляет к проверке готовности признак остановки.
func (l *lifecycle) ready(next func(ctx context.Context) error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if l.stopping.Load() {
			return errStopping
		}
		return next(ctx)
	}
}

// readyzHandler — GET /readyz: 200, пока проверка проходит; 503 — иначе.
func readyzHandler(log *slog.Logger, ready func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if ready == nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return
		}
		if err := ready(ctx); err != nil {
			if errors.Is(err, errStopping) {
				httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
				return
			}
			log.WarnContext(ctx, "readiness check failed", logx.KeyOp, "HTTP.Ready", logx.KeyError, err.Error())
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// livezHandler — GET /livez: процесс жив и обслуживает запросы.
func livezHandler(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// runProbe — maxim probe <url>: код 0, если адрес ответил 200 за probeTimeout.
// Конфигурацию не читает и журнал не пишет: вызывается каждые несколько секунд.
func runProbe(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: maxim probe <http(s)://host:port/path>")
		return 2
	}
	u, err := url.Parse(args[0])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintf(stderr, "probe: invalid url %q\n", args[0])
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return 2
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return 1
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "probe: %s -> %d\n", u.Redacted(), resp.StatusCode)
		return 1
	}
	return 0
}

// parseConfigCheck разбирает «check --for <mode>» и возвращает проверяемый режим.
func parseConfigCheck(args []string, out io.Writer) (string, error) {
	if len(args) == 0 || args[0] != "check" {
		return "", errUsage
	}
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(out)
	target := fs.String("for", "", "api | worker | migrate | legal | admin | dev")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return "", errUsage
	}
	if !slices.Contains(configCheckModes, *target) {
		return "", errUsage
	}
	return *target, nil
}
