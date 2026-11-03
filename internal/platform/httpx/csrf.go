package httpx

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// CSRF — защита от межсайтовой подделки двойной отправкой (R-4): меняющий
// запрос пользователя с сессией повторяет значение cookie csrf_token в
// заголовке X-CSRF-Token. exempt — запросы без проверки (вход, Webhook).
func CSRF(log *slog.Logger, exempt func(r *http.Request) bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if !Authenticated(r.Context()) || exempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			header := r.Header.Get(CSRFHeader)
			c, err := r.Cookie(CSRFCookie)
			if err != nil || header == "" || c.Value == "" ||
				subtle.ConstantTimeCompare([]byte(header), []byte(c.Value)) != 1 {
				log.WarnContext(r.Context(), "csrf check failed", logx.KeyOp, "HTTP.CSRF", "method", r.Method, "path", RedactPath(r.URL.Path))
				WriteError(w, r, log, apperr.Forbidden("csrf"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
