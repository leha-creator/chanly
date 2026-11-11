package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// JobSendLoginCode — задание отправки письма с кодом входа (R-1).
const JobSendLoginCode = "send_login_code"

const sendLoginCodeAttempts = 5

// loginCodePayload — полезная нагрузка задания. Почта и код — только в Sealed.
type loginCodePayload struct {
	AuthCodeID uuid.UUID `json:"auth_code_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	Sealed     string    `json:"sealed"`
}

type sealedLoginCode struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// RequestLoginCode создаёт код входа и ставит задание отправить письмо (S-01 шаг 1).
// Ответ не зависит от того, есть ли пользователь с этим адресом (07 §4).
func (s *Service) RequestLoginCode(ctx context.Context, email string, ip *netip.Addr) (time.Duration, error) {
	const op = "Account.RequestLoginCode"
	email = domain.NormalizeEmail(email)
	ipAttr := ipString(ip)
	s.log.DebugContext(ctx, "login code request started", logx.KeyOp, op, "email", logx.MaskEmail(email), "ip", ipAttr)

	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		since := now.Add(-time.Hour)
		byEmail, err := s.AuthCodes.CountByEmailSince(ctx, email, since)
		if err != nil {
			return err
		}
		if byEmail.Count >= s.Config.CodesPerEmailHour {
			return apperr.RateLimited("email", byEmail.Oldest.Add(time.Hour).Sub(now))
		}
		if ip != nil {
			byIP, err := s.AuthCodes.CountByIPSince(ctx, *ip, since)
			if err != nil {
				return err
			}
			if byIP.Count >= s.Config.CodesPerIPHour {
				return apperr.RateLimited("ip", byIP.Oldest.Add(time.Hour).Sub(now))
			}
		}
		// Действует только последний код адреса.
		if err := s.AuthCodes.ConsumeActive(ctx, email, now); err != nil {
			return err
		}
		code, err := domain.GenerateCode()
		if err != nil {
			return err
		}
		lc := domain.NewLoginCode(ids.New(), email, code, s.Config.Pepper, ip, s.Config.LoginCodeTTL, now)
		if err := s.AuthCodes.Create(ctx, lc); err != nil {
			return err
		}
		secret, err := json.Marshal(sealedLoginCode{Email: email, Code: code})
		if err != nil {
			return err
		}
		sealed, err := s.Sealer.Seal(secret)
		if err != nil {
			return err
		}
		return s.Jobs.Enqueue(ctx, job.Job{
			Kind:        JobSendLoginCode,
			DedupeKey:   "login_code:" + lc.ID.String(),
			Payload:     loginCodePayload{AuthCodeID: lc.ID, ExpiresAt: lc.ExpiresAt, Sealed: sealed},
			MaxAttempts: sendLoginCodeAttempts,
		})
	})
	if e, ok := apperr.As(err); ok && e.Code == apperr.CodeRateLimited {
		s.log.WarnContext(ctx, "login code rate limited", logx.KeyOp, op, "scope", e.Details["scope"],
			"email", logx.MaskEmail(email), "ip", ipAttr)
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	s.log.InfoContext(ctx, "login code requested", logx.KeyOp, op, "email", logx.MaskEmail(email), "ip", ipAttr)
	return s.Config.LoginCodeTTL, nil
}

// SendLoginCode — обработчик задания send_login_code. Повтор после успешной
// отправки ничего не делает: полезная нагрузка к тому времени затёрта.
func (s *Service) SendLoginCode(ctx context.Context, raw []byte) error {
	const op = "Account.SendLoginCode"
	var p loginCodePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.Sealed == "" {
		s.log.DebugContext(ctx, "login code already sent", logx.KeyOp, op)
		return nil
	}
	now := s.Clock.Now()
	if !now.Before(p.ExpiresAt) {
		s.log.WarnContext(ctx, "login code expired before sending", logx.KeyOp, op, "auth_code_id", p.AuthCodeID)
		return nil
	}
	plain, err := s.Sealer.Open(p.Sealed)
	if err != nil {
		return fmt.Errorf("open payload: %w", err)
	}
	var secret sealedLoginCode
	if err := json.Unmarshal(plain, &secret); err != nil {
		return fmt.Errorf("decode sealed payload: %w", err)
	}
	if err := s.Sender.SendLoginCode(ctx, secret.Email, secret.Code, p.ExpiresAt.Sub(now)); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "login code sent", logx.KeyOp, op, "auth_code_id", p.AuthCodeID)
	return nil
}

// LoginResult — итог входа.
type LoginResult struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
	SessionTTL   time.Duration
	Principal    Principal
	NewUser      bool
}

// Login проверяет код и открывает сессию (S-01 шаг 2). Пользователь создаётся
// при первом входе. Любая неудача — одинаковый ответ 401, причина — в журнале.
func (s *Service) Login(ctx context.Context, email, code string, ip *netip.Addr, userAgent string) (*LoginResult, error) {
	const op = "Account.Login"
	email = domain.NormalizeEmail(email)
	var (
		failure domain.LoginFailure
		result  *LoginResult
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		lc, err := s.AuthCodes.LatestActiveForUpdate(ctx, email)
		if errors.Is(err, domain.ErrNotFound) {
			failure = domain.FailureNoCode
			return nil
		}
		if err != nil {
			return err
		}
		if verr := lc.Verify(s.Config.Pepper, code, s.Config.LoginCodeAttempts, now); verr != nil {
			if !errors.As(verr, &failure) {
				return verr
			}
			if failure == domain.FailureWrongCode {
				return s.AuthCodes.Save(ctx, lc) // попытка учитывается и при неудаче
			}
			return nil
		}
		if err := s.AuthCodes.Save(ctx, lc); err != nil {
			return err
		}

		u, err := s.Users.GetByEmail(ctx, email)
		newUser := errors.Is(err, domain.ErrNotFound)
		switch {
		case newUser:
			u = domain.NewUser(ids.New(), email, now)
			if err := s.Users.Create(ctx, u); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if u.Blocked() {
			failure = domain.FailureBlocked
			return nil
		}

		token, hash, err := domain.NewToken()
		if err != nil {
			return err
		}
		ttl := domain.SessionTTL(u, s.Config.SessionTTL, s.Config.AdminSessionTTL)
		var ua *string
		if userAgent != "" {
			ua = &userAgent
		}
		sess := domain.NewSession(ids.New(), u.ID, hash, ip, ua, ttl, now)
		if err := s.Sessions.Create(ctx, sess); err != nil {
			return err
		}
		if err := s.Users.SetLastLogin(ctx, u.ID, now); err != nil {
			return err
		}
		p, err := s.principal(ctx, u, sess.ID)
		if err != nil {
			return err
		}
		ctx = audit.WithActor(ctx, audit.Actor{Type: p.actorType(), UserID: &u.ID, OrganizationID: p.OrganizationID, IP: ip})
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "auth.login", ObjectType: "user", ObjectID: u.ID.String(),
			Data: map[string]any{"session_id": sess.ID, "new_user": newUser},
		}); err != nil {
			return err
		}
		csrf, err := randomToken()
		if err != nil {
			return err
		}
		result = &LoginResult{SessionToken: token, CSRFToken: csrf, ExpiresAt: sess.ExpiresAt, SessionTTL: ttl, Principal: p, NewUser: newUser}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if failure != "" {
		s.log.WarnContext(ctx, "login failed", logx.KeyOp, op, "reason", string(failure),
			"email", logx.MaskEmail(email), "ip", ipString(ip))
		if failure == domain.FailureBlocked {
			return nil, apperr.Forbidden("user_blocked")
		}
		return nil, apperr.Unauthorized("code is invalid or expired")
	}
	s.log.InfoContext(logx.With(ctx, logx.KeyUserID, result.Principal.UserID.String()), "user logged in",
		logx.KeyOp, op, "new_user", result.NewUser, "ip", ipString(ip))
	return result, nil
}

// Authenticate находит сессию по токену из cookie и продлевает её (07 §4).
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	return s.authenticate(ctx, token, true)
}

// AuthenticateStream checks current access without extending an idle browser session.
func (s *Service) AuthenticateStream(ctx context.Context, token string) (*Principal, error) {
	return s.authenticate(ctx, token, false)
}

func (s *Service) authenticate(ctx context.Context, token string, touch bool) (*Principal, error) {
	const op = "Account.Authenticate"
	if token == "" {
		return nil, apperr.Unauthorized("session is missing")
	}
	now := s.Clock.Now()
	sess, err := s.Sessions.GetByTokenHash(ctx, domain.HashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		s.log.DebugContext(ctx, "unknown session", logx.KeyOp, op)
		return nil, apperr.Unauthorized("session is invalid")
	}
	if err != nil {
		return nil, err
	}
	if !sess.Active(now) {
		s.log.DebugContext(ctx, "session is not active", logx.KeyOp, op, "session_id", sess.ID)
		return nil, apperr.Unauthorized("session is expired")
	}
	u, err := s.Users.GetByID(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if u.Blocked() {
		s.log.WarnContext(ctx, "blocked user session rejected", logx.KeyOp, op, "user_id", u.ID)
		return nil, apperr.Unauthorized("user is blocked")
	}
	ttl := domain.SessionTTL(u, s.Config.SessionTTL, s.Config.AdminSessionTTL)
	if touch && sess.Touch(ttl, now) {
		if err := s.Sessions.SetExpiresAt(ctx, sess.ID, sess.ExpiresAt, now); err != nil {
			return nil, err
		}
		s.log.DebugContext(ctx, "session extended", logx.KeyOp, op, "session_id", sess.ID)
	}
	p, err := s.principal(ctx, u, sess.ID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Logout закрывает сессию.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	const op = "Account.Logout"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Sessions.Revoke(ctx, p.SessionID, s.Clock.Now()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: "auth.logout", ObjectType: "user", ObjectID: p.UserID.String(), ActorType: p.actorType(),
			Data: map[string]any{"session_id": p.SessionID},
		})
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "user logged out", logx.KeyOp, op, "session_id", p.SessionID)
	return nil
}

// principal собирает принципала: пользователь, организация и кабинеты.
func (s *Service) principal(ctx context.Context, u *domain.User, sessionID uuid.UUID) (Principal, error) {
	p := Principal{UserID: u.ID, SessionID: sessionID, AdminRole: u.AdminRole}
	o, err := s.Organizations.GetByUser(ctx, u.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return p, nil
	case err != nil:
		return Principal{}, err
	}
	p.OrganizationID = &o.ID
	p.Cabinets = o.Cabinets()
	return p, nil
}

// randomToken — токен CSRF для двойной отправки (R-4): не хранится на сервере.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ipString(ip *netip.Addr) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}
