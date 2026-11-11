// Command mockapi — тестовый сервер API кабинетов: данные выдуманы и живут
// в памяти до перезапуска. Не каркас продукта.
//
// Переменные окружения:
//
//	MOCKAPI_ADDR          — адрес, по умолчанию :8080
//	LOG_LEVEL             — debug (по умолчанию), info, warn, error
//	MOCKAPI_COOKIE_SECURE — true: cookie сессии с признаком Secure (стенд по HTTPS)
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"maxim/mockapi/internal/clock"
	"maxim/mockapi/internal/handlers"
	"maxim/mockapi/internal/logx"
	"maxim/mockapi/internal/seed"
	"maxim/mockapi/internal/server"
	"maxim/mockapi/internal/store"
)

// version задаётся при сборке: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	log := logx.New(os.Stdout, os.Getenv("LOG_LEVEL"))
	addr := os.Getenv("MOCKAPI_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	cookieSecure, _ := strconv.ParseBool(os.Getenv("MOCKAPI_COOKIE_SECURE"))

	st := store.New(clock.New(), log, seed.Load)
	handler, err := server.New(log, st, handlers.Options{CookieSecure: cookieSecure})
	if err != nil {
		log.Error("server init failed", "op", "Main.Start", "error", err.Error())
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(handler, 60<<20), // загрузка файлов — до 50 МБ
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("server started", "op", "Main.Start", "addr", addr, "version", version,
			"cookie_secure", cookieSecure, "docs", "http://localhost"+portOf(addr)+"/docs")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "op", "Main.Start", "error", err.Error())
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	st.CloseStreams()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown failed", "op", "Main.Stop", "error", err.Error())
		os.Exit(1)
	}
	log.Info("server stopped", "op", "Main.Stop")
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i:]
		}
	}
	return ""
}
