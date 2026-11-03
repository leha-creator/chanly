// Package httpx — общие части HTTP-слоя: промежуточные слои, ответ об ошибке
// в формате Error спецификации, сессия, CSRF, IP клиента.
package httpx

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// errorBody повторяет схему Error из api/openapi.yaml.
type errorBody struct {
	Error struct {
		Code    apperr.Code    `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// StatusOf переводит код ошибки в HTTP-статус.
func StatusOf(code apperr.Code) int {
	switch code {
	case apperr.CodeValidationFailed:
		return http.StatusUnprocessableEntity
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	case apperr.CodeForbidden:
		return http.StatusForbidden
	case apperr.CodeNotFound:
		return http.StatusNotFound
	case apperr.CodeRateLimited:
		return http.StatusTooManyRequests
	case apperr.CodeNotImplemented:
		return http.StatusNotImplemented
	case apperr.CodeServiceUnavailable:
		return http.StatusServiceUnavailable
	case apperr.CodeInternal:
		return http.StatusInternalServerError
	default: // conflict, inn_taken, invalid_transition и прочие предметные отказы
		return http.StatusConflict
	}
}

// WriteError пишет ответ об ошибке. Ошибка без кода считается непредвиденной:
// клиент получает 500 internal, подробности — только в журнале.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	ctx := r.Context()
	e, ok := apperr.As(err)
	if !ok {
		e = apperr.Internal(err)
	}
	status := StatusOf(e.Code)
	switch {
	case e.Code == apperr.CodeServiceUnavailable:
		log.WarnContext(ctx, "request rejected, service unavailable", logx.KeyOp, "HTTP.Error", "status", status,
			"reason", e.Details["reason"])
	case status >= 500 && e.Code != apperr.CodeNotImplemented:
		log.ErrorContext(ctx, "request failed", logx.KeyOp, "HTTP.Error", "status", status, logx.KeyError, err.Error())
	case status == http.StatusForbidden:
		log.WarnContext(ctx, "access denied", logx.KeyOp, "HTTP.Error", "method", r.Method, "path", RedactPath(r.URL.Path),
			"reason", e.Details["reason"])
	default:
		log.DebugContext(ctx, "request rejected", logx.KeyOp, "HTTP.Error", "status", status, "code", e.Code)
	}

	var body errorBody
	body.Error.Code = e.Code
	body.Error.Message = e.Message
	body.Error.Details = e.Details
	body.RequestID = logx.RequestID(ctx)
	if e.Code == apperr.CodeRateLimited && e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(e.RetryAfter.Seconds()))))
	}
	WriteJSON(w, status, body)
}

// WriteJSON пишет тело JSON с указанным статусом.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
