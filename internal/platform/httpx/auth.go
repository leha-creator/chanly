package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Имена cookie (07 §4, R-4).
const (
	SessionCookie = "session"
	CSRFCookie    = "csrf_token"
	CSRFHeader    = "X-CSRF-Token"
)

// Principal — пользователь запроса. Модули получают его из context.
type Principal struct {
	UserID         uuid.UUID
	SessionID      uuid.UUID
	OrganizationID *uuid.UUID
	Cabinets       []string
	AdminRole      string
}

// Authenticator находит пользователя по токену сессии. Ошибка с кодом
// unauthorized — сессии нет; другая ошибка — сбой.
type Authenticator func(ctx context.Context, token string) (*Principal, error)

type principalKey struct{}

// PrincipalFrom возвращает пользователя запроса.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// WithPrincipal кладёт пользователя в context (для тестов и служебных вызовов).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Session читает cookie сессии и кладёт пользователя в context, поля журнала
// и действующее лицо журнала действий. Без сессии запрос идёт дальше: требование
// сессии проверяет слой спецификации (security), чтобы открытые операции работали.
func Session(log *slog.Logger, authn Authenticator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			p, err := authn(ctx, c.Value)
			if err != nil {
				if apperr.Is(err, apperr.CodeUnauthorized) {
					log.DebugContext(ctx, "session rejected", logx.KeyOp, "HTTP.Session")
					next.ServeHTTP(w, r)
					return
				}
				WriteError(w, r, log, err)
				return
			}
			ctx = WithPrincipal(ctx, p)
			ctx = logx.With(ctx, logx.KeyUserID, p.UserID.String())
			if p.OrganizationID != nil {
				ctx = logx.With(ctx, logx.KeyOrganizationID, p.OrganizationID.String())
			}
			actor := audit.ActorFrom(ctx)
			actor.UserID, actor.OrganizationID = &p.UserID, p.OrganizationID
			ctx = audit.WithActor(ctx, actor)
			log.DebugContext(ctx, "principal resolved", logx.KeyOp, "HTTP.Session", "cabinets", p.Cabinets)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticated сообщает, что у запроса есть пользователь.
func Authenticated(ctx context.Context) bool {
	_, ok := PrincipalFrom(ctx)
	return ok
}

// ReasonCabinetRequired — у организации нет нужного кабинета.
const ReasonCabinetRequired = "cabinet_required"

// RequireCabinet проверяет, что у организации пользователя открыт кабинет.
func RequireCabinet(ctx context.Context, cabinet string) (*Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, apperr.Unauthorized("session is missing")
	}
	if !slices.Contains(p.Cabinets, cabinet) {
		return nil, apperr.Forbidden(ReasonCabinetRequired).WithDetail("cabinet", cabinet)
	}
	return p, nil
}

// ReasonAdminRequired — у пользователя нет нужной роли администратора (07 §4).
const ReasonAdminRequired = "admin_required"

// RequireAdmin проверяет, что у пользователя одна из ролей администратора roles:
// 401 без сессии, 403 с details.reason = admin_required без роли. Отказ пишется в журнал.
func RequireAdmin(ctx context.Context, log *slog.Logger, op string, roles ...string) (*Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, apperr.Unauthorized("session is missing")
	}
	if p.AdminRole == "" || !slices.Contains(roles, p.AdminRole) {
		log.WarnContext(ctx, "admin access denied", logx.KeyOp, op, logx.KeyUserID, p.UserID, "required_roles", roles)
		return nil, apperr.Forbidden(ReasonAdminRequired)
	}
	return p, nil
}

// Роли администратора (07 §4) и матрица операций (R-1 этапа «API администратора»).
const (
	AdminRoleModerator = "moderator"
	AdminRoleSenior    = "senior"
)

var (
	// AdminAny — операция доступна обеим ролям: модерация, споры, чтение, ошибки ОРД.
	AdminAny = []string{AdminRoleModerator, AdminRoleSenior}
	// AdminSeniorOnly — только senior: деньги, блокировки, справочники, журнал действий.
	AdminSeniorOnly = []string{AdminRoleSenior}
)

// IsSenior — у пользователя роль senior.
func IsSenior(p *Principal) bool { return p != nil && p.AdminRole == AdminRoleSenior }

// INNFor — ИНН для ответа администратору: полный у senior, с маской у moderator (R-14).
func INNFor(p *Principal, inn string) string {
	if IsSenior(p) {
		return inn
	}
	return logx.MaskINN(inn)
}

// EmailFor — почта для ответа администратору: полная у senior, с маской у moderator.
func EmailFor(p *Principal, email string) string {
	if IsSenior(p) {
		return email
	}
	return logx.MaskEmail(email)
}

// PhoneFor — телефон для ответа администратору: полный у senior, с маской у moderator.
func PhoneFor(p *Principal, phone string) string {
	if IsSenior(p) {
		return phone
	}
	return logx.MaskPhone(phone)
}

// AdminContext — RequireAdmin и context с действующим лицом admin для audit_log: операции
// администратора через HTTP пишут журнал действий так же, как команды maxim admin.
func AdminContext(ctx context.Context, log *slog.Logger, op string, roles ...string) (context.Context, *Principal, error) {
	p, err := RequireAdmin(ctx, log, op, roles...)
	if err != nil {
		return ctx, nil, err
	}
	actor := audit.ActorFrom(ctx)
	actor.Type, actor.UserID, actor.OrganizationID = audit.ActorAdmin, &p.UserID, nil
	return audit.WithActor(ctx, actor), p, nil
}

// AdminGate отказывает пользователю без роли администратора на путях prefix до проверки по
// спецификации (этап «API администратора», R-1): иначе ответ 422 о полях тела раскрывал бы
// контракт операций администратора. Без сессии запрос идёт дальше — 401 даёт спецификация;
// роль moderator или senior для конкретной операции проверяет обработчик.
func AdminGate(prefix string, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := PrincipalFrom(r.Context()); ok && p.AdminRole == "" &&
				(r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")) {
				log.WarnContext(r.Context(), "admin access denied", logx.KeyOp, "HTTP.AdminGate", logx.KeyUserID, p.UserID)
				WriteError(w, r, log, apperr.Forbidden(ReasonAdminRequired))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
