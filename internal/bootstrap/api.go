package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
)

const (
	apiShutdownTimeout = 15 * time.Second
	readHeaderTimeout  = 10 * time.Second
)

// namedServer — HTTP-сервер с именем для журнала.
type namedServer struct {
	name string
	srv  *http.Server
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// metricsServer — порт метрик. С ready на нём же /livez и /readyz: у worker нет другого
// HTTP-порта, а HEALTHCHECK compose проверяет готовность (deploy/app/compose.yml).
func metricsServer(addr string, reg *metrics.Registry, log *slog.Logger, ready func(ctx context.Context) error) namedServer {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", reg.Handler())
	if ready != nil {
		mux.HandleFunc("GET /livez", livezHandler)
		mux.HandleFunc("GET /readyz", readyzHandler(log, ready))
	}
	return namedServer{name: "metrics", srv: newHTTPServer(addr, mux)}
}

// stopPolicy — порядок остановки серверов: признак остановки для /readyz и пауза до Shutdown,
// за которую прокси убирает экземпляр (HTTP_SHUTDOWN_DRAIN, 07 §8).
type stopPolicy struct {
	life  *lifecycle
	drain time.Duration
}

// serve запускает серверы и останавливает их плавно, когда отменяется ctx.
func serve(ctx context.Context, log *slog.Logger, stop stopPolicy, servers ...namedServer) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, s := range servers {
		ln, err := net.Listen("tcp", s.srv.Addr)
		if err != nil {
			return fmt.Errorf("listen %s on %s: %w", s.name, s.srv.Addr, err)
		}
		log.Info("component ready", logx.KeyOp, "Bootstrap.Serve", "component", s.name, "addr", ln.Addr().String())
		g.Go(func() error {
			if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("%s server: %w", s.name, err)
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		if stop.life != nil {
			stop.life.stopping.Store(true)
		}
		if stop.drain > 0 {
			// /readyz уже отвечает 503; новые запросы ещё принимаются, пока прокси не убрал экземпляр.
			log.Info("drain started", logx.KeyOp, "Bootstrap.Serve", "drain_ms", stop.drain.Milliseconds())
			time.Sleep(stop.drain)
		}
		start := time.Now()
		log.Info("shutdown started", logx.KeyOp, "Bootstrap.Serve")
		sctx, cancel := context.WithTimeout(context.Background(), apiShutdownTimeout)
		defer cancel()
		var errs []error
		for _, s := range servers {
			if err := s.srv.Shutdown(sctx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown %s: %w", s.name, err))
			}
		}
		log.Info("shutdown finished", logx.KeyOp, "Bootstrap.Serve", "duration_ms", time.Since(start).Milliseconds())
		return errors.Join(errs...)
	})
	return g.Wait()
}

// runAPI — режим api: HTTP API кабинетов и метрики.
func runAPI(ctx context.Context, cfg Config, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	reg := metrics.New()
	in, err := newInfra(ctx, cfg, ModeAPI, pool, log, reg)
	if err != nil {
		return err
	}
	reg.MustRegister(inbound.NewCollector(pool, in.clock, log.With(logx.KeyModule, "inbound")))
	m := newModules(cfg, in)
	if err := m.startMarking(ctx); err != nil {
		return err
	}
	h, err := apiHandler(cfg, in, m, reg)
	if err != nil {
		return err
	}
	return serve(ctx, log, stopPolicy{life: &in.life, drain: cfg.ShutdownDrain},
		namedServer{name: "api", srv: newHTTPServer(cfg.HTTPAddr, h)},
		metricsServer(cfg.MetricsAddr, reg, nil, nil))
}
