package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	middleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
)

// BaseURL — префикс API, совпадает с servers в спецификации.
const BaseURL = "/api/v1"

// Пределы тела запроса: JSON — 1 МБ, документ о режиме налогообложения — 10 МБ
// плюс запас на разметку multipart (R-5), изображение медиатеки — 50 МБ плюс запас
// (этап «Медиатека и пост», R-2).
const (
	bodyLimitDefault     = 1 << 20
	bodyLimitTaxDocument = 11 << 20
	bodyLimitStatement   = 11 << 20
	bodyLimitMedia       = 51 << 20
)

// fallback опускает заглушки на уровень глубже обработчиков модулей: при
// встраивании в один тип методы модулей перекрывают методы заглушки (R-7).
type fallback struct{ unimplemented }

// serverDeps — всё, из чего собирается HTTP-обработчик API.
type serverDeps struct {
	Log     *slog.Logger
	Clock   clock.Clock
	Metrics *metrics.Registry
	// Impl — реализация всех операций: обработчики модулей поверх fallback.
	Impl apigen.StrictServerInterface
	// Ready проверяет готовность принимать запросы (связь с базой) для /readyz.
	Ready func(ctx context.Context) error
	// Authenticated сообщает, что запрос несёт действующую сессию. Проверяется
	// для операций, у которых в спецификации есть security.
	Authenticated func(ctx context.Context) bool
	// Before — слои между общими и проверкой по спецификации: сессия, CSRF.
	Before []httpx.Middleware
	// ServeDocs включает /openapi.json и /docs (не в продакшене).
	ServeDocs bool
	// Routes — обработчики вне strict server: шаблон ServeMux → обработчик (Webhook, R-4).
	// Слои Before к ним не применяются.
	Routes map[string]http.Handler
}

// newHandler собирает корневой HTTP-обработчик.
func newHandler(d serverDeps) (http.Handler, error) {
	spec, err := loadSpec()
	if err != nil {
		return nil, err
	}
	log := d.Log.With(logx.KeyModule, "http")

	strict := apigen.NewStrictHandlerWithOptions(d.Impl,
		[]apigen.StrictMiddlewareFunc{routeMiddleware},
		apigen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				log.WarnContext(r.Context(), "request validation failed", logx.KeyOp, "HTTP.Decode", "field", "body")
				httpx.WriteError(w, r, log, apperr.Validation("body", "malformed").WithCause(err))
			},
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				httpx.WriteError(w, r, log, err)
			},
		})
	apiHandler := apigen.HandlerWithOptions(strict, apigen.StdHTTPServerOptions{
		BaseURL: BaseURL,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			log.WarnContext(r.Context(), "request validation failed", logx.KeyOp, "HTTP.Bind", "field", "parameters")
			httpx.WriteError(w, r, log, apperr.Validation("parameters", "invalid").WithCause(err))
		},
	})

	authenticated := d.Authenticated
	if authenticated == nil {
		authenticated = func(context.Context) bool { return false }
	}
	validator := middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		DoNotValidateServers: true,
		Prefix:               BaseURL,
		Options: openapi3filter.Options{
			MultiError: false,
			AuthenticationFunc: func(ctx context.Context, in *openapi3filter.AuthenticationInput) error {
				if !authenticated(in.RequestValidationInput.Request.Context()) {
					return errors.New("session is missing or invalid")
				}
				return nil
			},
		},
		ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, opts middleware.ErrorHandlerOpts) {
			if opts.MatchedRoute != nil && opts.MatchedRoute.Route != nil && opts.MatchedRoute.Route.Operation != nil {
				httpx.SetRoute(r.Context(), opts.MatchedRoute.Route.Operation.OperationID)
			}
			switch {
			case opts.MatchedRoute == nil:
				httpx.WriteError(w, r, log, apperr.NotFound("no such API operation"))
			case opts.StatusCode == http.StatusUnauthorized:
				log.WarnContext(r.Context(), "unauthorized request", logx.KeyOp, "HTTP.Auth", "path", httpx.RedactPath(r.URL.Path))
				httpx.WriteError(w, r, log, apperr.Unauthorized("session is missing or invalid"))
			default:
				field, reason := validationField(err)
				log.WarnContext(r.Context(), "request validation failed", logx.KeyOp, "HTTP.Validate", "field", field)
				httpx.WriteError(w, r, log, apperr.Validation(field, reason).WithCause(err))
			}
		},
	})

	var apiChain http.Handler = validator(apiHandler)
	// Пользователь без роли администратора получает 403 на /admin/* до проверки тела (R-1).
	apiChain = httpx.AdminGate(BaseURL+"/admin", log)(apiChain)
	apiChain = httpx.Chain(apiChain, d.Before...)

	mux := http.NewServeMux()
	mux.Handle(BaseURL+"/", apiChain)
	for pattern, h := range d.Routes {
		mux.Handle(pattern, h)
	}
	mux.HandleFunc("GET /livez", livezHandler)
	mux.HandleFunc("GET /readyz", readyzHandler(log, d.Ready))
	if d.ServeDocs {
		specJSON, err := json.Marshal(spec)
		if err != nil {
			return nil, fmt.Errorf("marshal spec: %w", err)
		}
		mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(specJSON)
		})
		mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(swaggerPage))
		})
	}

	return httpx.Chain(mux,
		httpx.RequestID(),
		httpx.AccessLog(log, d.Clock, d.Metrics),
		httpx.Recover(log),
		httpx.SecurityHeaders(BaseURL),
		httpx.BodyLimit(func(r *http.Request) int64 {
			// Выписка банка — до STATEMENT_MAX_BYTES (10 МБ) и заголовки multipart.
			if r.Method == http.MethodPost && r.URL.Path == BaseURL+"/admin/bank-statements" {
				return bodyLimitStatement
			}
			if r.URL.Path == BaseURL+"/organizations/current/tax-document" {
				return bodyLimitTaxDocument
			}
			if r.Method == http.MethodPost && r.URL.Path == BaseURL+"/media" {
				return bodyLimitMedia
			}
			return bodyLimitDefault
		}),
	), nil
}

// routeMiddleware сообщает журналу и метрикам operationId операции.
func routeMiddleware(f apigen.StrictHandlerFunc, operationID string) apigen.StrictHandlerFunc {
	route := strings.ToLower(operationID[:1]) + operationID[1:]
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		httpx.SetRoute(ctx, route)
		return f(ctx, w, r, request)
	}
}

// loadSpec возвращает встроенную спецификацию без операций тестового сервера (тег _mock).
func loadSpec() (*openapi3.T, error) {
	spec, err := apigen.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}
	for path, item := range spec.Paths.Map() {
		mockOnly := true
		for _, op := range item.Operations() {
			if !slices.Contains(op.Tags, "_mock") {
				mockOnly = false
			}
		}
		if mockOnly {
			spec.Paths.Delete(path)
		}
	}
	spec.Tags = slices.DeleteFunc(spec.Tags, func(t *openapi3.Tag) bool { return t.Name == "_mock" })
	return spec, nil
}

// validationField достаёт из ошибки проверки имя поля и причину. Значение поля
// в ответ и журнал не попадает.
func validationField(err error) (field, reason string) {
	field, reason = "request", "invalid"
	var re *openapi3filter.RequestError
	if !errors.As(err, &re) {
		return field, reason
	}
	if re.Parameter != nil {
		field = re.Parameter.Name
	} else if re.RequestBody != nil {
		field = "body"
	}
	var se *openapi3.SchemaError
	if errors.As(re.Err, &se) {
		if p := se.JSONPointer(); len(p) > 0 {
			field = strings.Join(p, ".")
		}
		if se.SchemaField != "" {
			reason = se.SchemaField
		}
	}
	return field, reason
}

const swaggerPage = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Chanly — API кабинетов</title>
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui.min.css">
  <style>body { margin: 0; background: #fff; }</style>
</head>
<body>
  <div id="ui"></div>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui-bundle.min.js"></script>
  <script>
    function csrf() {
      const m = document.cookie.match(/(?:^|; )csrf_token=([^;]+)/);
      return m ? decodeURIComponent(m[1]) : "";
    }
    window.ui = SwaggerUIBundle({
      url: "/openapi.json",
      dom_id: "#ui",
      deepLinking: true,
      displayRequestDuration: true,
      tryItOutEnabled: true,
      requestInterceptor: (req) => {
        req.credentials = "same-origin";
        if (req.method !== "GET") { req.headers["X-CSRF-Token"] = csrf(); }
        return req;
      }
    });
  </script>
</body>
</html>
`
