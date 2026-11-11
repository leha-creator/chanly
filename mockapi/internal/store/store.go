// Package store — хранилище тестового сервера в памяти: данные, машина
// состояний размещения (03 §4), таймеры (03 §5) и движения денег.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/clock"
	"maxim/mockapi/internal/logx"
)

// SeedFunc заполняет пустое состояние начальными данными.
type SeedFunc func(d *Data, now time.Time)

// Store защищает данные одним мьютексом: заглушка однопоточна по смыслу,
// почти каждый запрос может сработать таймером и изменить данные.
type Store struct {
	mu       sync.Mutex
	data     *Data
	sessions map[string]*Session
	clock    *clock.Clock
	log      *slog.Logger
	seed     SeedFunc
	streams  map[*Stream]struct{}
}

// New создаёт хранилище и загружает начальные данные.
func New(c *clock.Clock, log *slog.Logger, seed SeedFunc) *Store {
	s := &Store{sessions: map[string]*Session{}, clock: c, log: log.With("module", "store"), seed: seed}
	s.load(context.Background())
	return s
}

func (s *Store) load(ctx context.Context) {
	d := NewData()
	s.seed(d, s.clock.Now())
	s.data = d
	logx.From(ctx, s.log).Info("seed loaded", "op", "Store.Load",
		"users", len(d.Users), "organizations", len(d.Orgs), "channels", len(d.Channels),
		"campaigns", len(d.Campaigns), "orders", len(d.Orders), "placements", len(d.Placements),
		"invoices", len(d.Invoices), "payouts", len(d.Payouts), "notifications", len(d.Notifications))
}

// Tx — доступ к данным под блокировкой. Создаётся только через Store.Begin.
type Tx struct {
	s   *Store
	ctx context.Context
	D   *Data
	Now time.Time
	// Fired — переходы, выполненные таймерами при открытии транзакции.
	Fired []TimerResult
	log   *slog.Logger
}

// Begin берёт блокировку, выполняет просроченные таймеры и возвращает Tx.
// Вызывающий обязан вызвать возвращённую функцию освобождения.
func (s *Store) Begin(ctx context.Context) (*Tx, func()) {
	s.mu.Lock()
	var before map[string]streamSnapshot
	if len(s.streams) > 0 {
		before = s.realtimeSnapshot()
	}
	tx := &Tx{s: s, ctx: ctx, D: s.data, Now: s.clock.Now(), log: logx.From(ctx, s.log)}
	tx.Fired = tx.RunTimers()
	return tx, func() {
		if before != nil {
			s.publishChanges(before)
		}
		s.mu.Unlock()
	}
}

// Reset возвращает начальные данные и убирает сдвиг часов. Сессии сохраняются.
func (s *Store) Reset(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock.Reset()
	s.load(ctx)
	s.resync()
	logx.From(ctx, s.log).Info("mock state reset", "op", "Store.Reset")
}

// Clock возвращает часы хранилища.
func (s *Store) Clock() *clock.Clock { return s.clock }

// CreateSession выдаёт случайный токен сессии (07 §4: не короче 256 бит).
func (tx *Tx) CreateSession(userID uuid.UUID) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	token := hex.EncodeToString(b)
	ttl := 30 * 24 * time.Hour
	if u := tx.D.Users[userID]; u != nil && u.AdminRole != "" {
		ttl = 12 * time.Hour
	}
	tx.s.sessions[token] = &Session{Token: token, UserID: userID, CreatedAt: tx.Now, ExpiresAt: tx.Now.Add(ttl)}
	return token
}

// DeleteSession закрывает сессию.
func (tx *Tx) DeleteSession(token string) {
	delete(tx.s.sessions, token)
}

// SessionUser находит пользователя по токену сессии без полной блокировки
// данных — используется промежуточным слоем до обработчика.
func (s *Store) SessionUser(token string) (uuid.UUID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok || !s.clock.Now().Before(sess.ExpiresAt) {
		return uuid.Nil, false
	}
	if u, ok := s.data.Users[sess.UserID]; !ok || u.Blocked {
		return uuid.Nil, false
	}
	return sess.UserID, true
}

// NewID создаёт идентификатор UUID версии 7 (04 §1).
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// Error — ошибка с HTTP-статусом и кодом из схемы ErrorCode.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NotFound(kind string, id any) *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: kind + " not found",
		Details: map[string]any{"object_type": kind, "id": fmt.Sprint(id)}}
}

func Validation(field, message string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: message,
		Details: map[string]any{"field": field}}
}

// Invalid — 422 с полем и кодом причины в details.reason, как у бэкенда.
func Invalid(field, reason, message string) *Error {
	e := Validation(field, message)
	e.Details["reason"] = reason
	return e
}

// ConflictReason — 409 conflict с кодом причины в details.reason.
func ConflictReason(reason, message string, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	details["reason"] = reason
	return Conflict("conflict", message, details)
}

func Conflict(code, message string, details map[string]any) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: message, Details: details}
}

func Forbidden(message string) *Error {
	return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: message}
}

func Unauthorized(message string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: message}
}

// notFoundDebug пишет промах поиска на уровне DEBUG и возвращает ошибку 404.
func (tx *Tx) notFound(kind string, id any) *Error {
	tx.log.Debug("object not found", "op", "Store.Find", "object_type", kind, "id", fmt.Sprint(id))
	return NotFound(kind, id)
}

// ---------------------------------------------------------------- поиск

func (tx *Tx) Org(id uuid.UUID) (*Organization, error) {
	if o, ok := tx.D.Orgs[id]; ok {
		return o, nil
	}
	return nil, tx.notFound("organization", id)
}

func (tx *Tx) Channel(id uuid.UUID) (*Channel, error) {
	for _, c := range tx.D.Channels {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, tx.notFound("channel", id)
}

// OrgChannel ищет канал блогерской организации; чужой канал — 404 (07 §4).
func (tx *Tx) OrgChannel(orgID, id uuid.UUID) (*Channel, error) {
	c, err := tx.Channel(id)
	if err != nil || c.OrgID == nil || *c.OrgID != orgID {
		return nil, tx.denied("channel", id, orgID)
	}
	return c, nil
}

func (tx *Tx) Format(id string) *Format {
	for _, f := range tx.D.Formats {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (tx *Tx) Category(id uuid.UUID) *Category {
	for _, c := range tx.D.Categories {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (tx *Tx) Campaign(orgID, id uuid.UUID) (*Campaign, error) {
	for _, c := range tx.D.Campaigns {
		if c.ID == id {
			if c.OrgID != orgID {
				return nil, tx.denied("campaign", id, orgID)
			}
			return c, nil
		}
	}
	return nil, tx.notFound("campaign", id)
}

func (tx *Tx) Order(id uuid.UUID) (*Order, error) {
	for _, o := range tx.D.Orders {
		if o.ID == id {
			return o, nil
		}
	}
	return nil, tx.notFound("order", id)
}

func (tx *Tx) OrgOrder(orgID, id uuid.UUID) (*Order, error) {
	o, err := tx.Order(id)
	if err != nil {
		return nil, err
	}
	if o.OrgID != orgID {
		return nil, tx.denied("order", id, orgID)
	}
	return o, nil
}

func (tx *Tx) Placement(id uuid.UUID) (*Placement, error) {
	for _, p := range tx.D.Placements {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, tx.notFound("placement", id)
}

func (tx *Tx) OrderPlacements(orderID uuid.UUID) []*Placement {
	var out []*Placement
	for _, p := range tx.D.Placements {
		if p.OrderID == orderID {
			out = append(out, p)
		}
	}
	return out
}

func (tx *Tx) Media(orgID, id uuid.UUID) (*MediaFile, error) {
	for _, m := range tx.D.Media {
		if m.ID == id {
			if m.OrgID != orgID {
				return nil, tx.denied("media_file", id, orgID)
			}
			return m, nil
		}
	}
	return nil, tx.notFound("media_file", id)
}

func (tx *Tx) Invoice(orgID, id uuid.UUID) (*Invoice, error) {
	for _, i := range tx.D.Invoices {
		if i.ID == id {
			if i.OrgID != orgID {
				return nil, tx.denied("invoice", id, orgID)
			}
			return i, nil
		}
	}
	return nil, tx.notFound("invoice", id)
}

// denied — объект есть, но принадлежит другой организации. Наружу — 404.
func (tx *Tx) denied(kind string, id any, orgID uuid.UUID) *Error {
	tx.log.Warn("access to foreign object denied", "op", "Store.Access",
		"object_type", kind, "id", fmt.Sprint(id), "organization_id", orgID)
	return NotFound(kind, id)
}

// OrgUsers возвращает пользователей организации — адресатов уведомлений.
func (tx *Tx) OrgUsers(orgID uuid.UUID) []*User {
	var out []*User
	for _, u := range tx.D.Users {
		if u.OrgID != nil && *u.OrgID == orgID {
			out = append(out, u)
		}
	}
	return out
}

// Notify создаёт уведомление в кабинете всем пользователям организации. Коды шаблонов — из
// каталога уведомлений бэкенда (04 §8); link — путь в кабинете, как у бэкенда.
func (tx *Tx) Notify(orgID uuid.UUID, template, title, text, objectType string, objectID uuid.UUID, orderID *uuid.UUID, link string) {
	for _, u := range tx.OrgUsers(orgID) {
		ot, oid := objectType, objectID
		tx.D.Notifications = append(tx.D.Notifications, &Notification{
			ID: NewID(), UserID: u.ID, Template: template, Title: title, Text: text,
			ObjectType: &ot, ObjectID: &oid, OrderID: orderID, Link: link, CreatedAt: tx.Now,
		})
	}
}

// Log возвращает журнал транзакции.
func (tx *Tx) Log() *slog.Logger { return tx.log }

// ClosingAct возвращает закрывающие документы организации.
func (tx *Tx) ClosingAct(orgID, id uuid.UUID) (*ClosingAct, error) {
	for _, a := range tx.D.ClosingActs {
		if a.ID == id {
			if a.OrgID != orgID {
				return nil, tx.denied("closing_act", id, orgID)
			}
			return a, nil
		}
	}
	return nil, tx.notFound("closing_act", id)
}
