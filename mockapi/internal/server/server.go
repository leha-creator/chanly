// Package server собирает HTTP-обработчик тестового сервера: промежуточные
// слои, проверку запросов по спецификации, API и страницу Swagger UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"
	middleware "github.com/oapi-codegen/nethttp-middleware"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/handlers"
	"maxim/mockapi/internal/logx"
	"maxim/mockapi/internal/store"
)

// BaseURL — префикс API, совпадает с servers в спецификации.
const BaseURL = "/api/v1"

// New возвращает корневой обработчик.
func New(log *slog.Logger, st *store.Store, opts handlers.Options) (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}
	log = log.With("module", "http")

	h := handlers.New(log, st, opts)
	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, r, &store.Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: err.Error()})
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			var se *store.Error
			if errors.As(err, &se) {
				writeError(w, r, se)
				return
			}
			logx.From(r.Context(), log).Error("handler failed", "op", "HTTP.Handle", "error", err.Error())
			writeError(w, r, &store.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"})
		},
	})
	apiHandler := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: BaseURL})

	validator := middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		DoNotValidateServers: true,
		Prefix:               BaseURL,
		Options: openapi3filter.Options{
			AuthenticationFunc: func(ctx context.Context, in *openapi3filter.AuthenticationInput) error {
				if _, ok := handlers.UserFrom(in.RequestValidationInput.Request.Context()); !ok {
					return errors.New("session cookie is missing or invalid")
				}
				return nil
			},
		},
		ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, opts middleware.ErrorHandlerOpts) {
			e := &store.Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "request does not match the API specification",
				Details: map[string]any{"reason": err.Error()}}
			switch {
			case opts.StatusCode == http.StatusUnauthorized:
				e = store.Unauthorized("session cookie is missing or invalid")
			case opts.MatchedRoute == nil:
				e = &store.Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such API operation"}
			}
			logx.From(r.Context(), log).Debug("request rejected by spec validation", "op", "HTTP.Validate",
				"status", e.Status, "reason", err.Error())
			writeError(w, r, e)
		},
	})

	mux := http.NewServeMux()
	mux.Handle(BaseURL+"/", validator(apiHandler))
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(specJSON)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerPage))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusFound)
	})

	var root http.Handler = mux
	root = sessionMiddleware(log, st, root)
	root = accessLog(log, root)
	root = recoverer(log, root)
	root = requestID(root)
	return root, nil
}

// requestID создаёт сквозной идентификатор или берёт его из X-Request-Id.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 100 {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(logx.WithRequestID(r.Context(), id)))
	})
}

// recoverer превращает панику в ответ 500 в формате Error.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logx.From(r.Context(), log).Error("panic recovered", "op", "HTTP.Recover",
					"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				writeError(w, r, &store.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// accessLog пишет строку журнала на каждый запрос.
func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		attrs := []any{"op", "HTTP.Request", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds()}
		if id, ok := handlers.UserFrom(r.Context()); ok {
			attrs = append(attrs, "user_id", id)
		}
		l := logx.From(r.Context(), log)
		if sw.status >= 500 {
			l.Error("request served", attrs...)
		} else {
			l.Info("request served", attrs...)
		}
	})
}

// sessionMiddleware находит пользователя по cookie session. Токен в журнал не пишется.
func sessionMiddleware(log *slog.Logger, st *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(handlers.SessionCookie); err == nil && c.Value != "" {
			if userID, ok := st.SessionUser(c.Value); ok {
				logx.From(r.Context(), log).Debug("session resolved", "op", "HTTP.Session", "user_id", userID)
				r = r.WithContext(handlers.WithSession(r.Context(), userID, c.Value))
			} else {
				logx.From(r.Context(), log).Debug("unknown session cookie", "op", "HTTP.Session")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, r *http.Request, e *store.Error) {
	if e.Code == "rate_limited" {
		w.Header().Set("Retry-After", fmt.Sprint(e.Details["retry_after"]))
	}
	body := api.Error{RequestId: logx.RequestID(r.Context()), Error: api.ErrorBody{Code: api.ErrorCode(e.Code), Message: e.Message}}
	if e.Details != nil {
		body.Error.Details = &e.Details
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}

const swaggerPage = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Тестовый API кабинетов</title>
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui.min.css">
  <style>body { margin: 0; background: #fff; }</style>
</head>
<body>
  <div id="ui"></div>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui-bundle.min.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "/openapi.json",
      dom_id: "#ui",
      deepLinking: true,
      persistAuthorization: true,
      displayRequestDuration: true,
      tryItOutEnabled: true,
      requestInterceptor: (req) => { req.credentials = "same-origin"; return req; }
    });
  </script>
</body>
</html>
`
