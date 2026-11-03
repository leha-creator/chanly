// Package adminref — наименования и ИНН организаций, почта пользователей по идентификаторам для
// ответов операций администратора (ссылки AdminOrganizationRef, AdminUserRef). Только чтение;
// маскирование по роли — в слое HTTP (httpx.INNFor, httpx.EmailFor).
package adminref

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/db"
)

// Org — организация в ссылке.
type Org struct {
	ID   uuid.UUID
	Name string
	INN  string
}

// Directory — ссылки на организации и пользователей.
type Directory interface {
	Orgs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Org, error)
	// Emails — почта пользователей.
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Store — Directory над таблицами organization и "user".
type Store struct{ pool *pgxpool.Pool }

// New создаёт справочник.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Orgs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Org, error) {
	out := make(map[uuid.UUID]Org, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Conn(ctx, s.pool).Query(ctx, `SELECT id, name, inn FROM organization WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("admin refs organizations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.INN); err != nil {
			return nil, fmt.Errorf("admin refs organizations: %w", err)
		}
		out[o.ID] = o
	}
	return out, rows.Err()
}

func (s *Store) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Conn(ctx, s.pool).Query(ctx, `SELECT id, email::text FROM "user" WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("admin refs users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, fmt.Errorf("admin refs users: %w", err)
		}
		out[id] = email
	}
	return out, rows.Err()
}

// Collector собирает идентификаторы для одного запроса к справочнику.
type Collector struct {
	orgs  map[uuid.UUID]struct{}
	users map[uuid.UUID]struct{}
}

// Org добавляет организацию.
func (c *Collector) Org(id uuid.UUID) {
	if c.orgs == nil {
		c.orgs = map[uuid.UUID]struct{}{}
	}
	if id != uuid.Nil {
		c.orgs[id] = struct{}{}
	}
}

// User добавляет пользователя; nil пропускается.
func (c *Collector) User(id *uuid.UUID) {
	if c.users == nil {
		c.users = map[uuid.UUID]struct{}{}
	}
	if id != nil && *id != uuid.Nil {
		c.users[*id] = struct{}{}
	}
}

// Refs — найденные ссылки.
type Refs struct {
	Orgs   map[uuid.UUID]Org
	Emails map[uuid.UUID]string
}

// Org — организация; неизвестная — только с идентификатором.
func (r Refs) Org(id uuid.UUID) Org {
	if o, ok := r.Orgs[id]; ok {
		return o
	}
	return Org{ID: id}
}

// Resolve читает собранные ссылки.
func (c *Collector) Resolve(ctx context.Context, d Directory) (Refs, error) {
	orgs, err := d.Orgs(ctx, keys(c.orgs))
	if err != nil {
		return Refs{}, err
	}
	emails, err := d.Emails(ctx, keys(c.users))
	if err != nil {
		return Refs{}, err
	}
	return Refs{Orgs: orgs, Emails: emails}, nil
}

func keys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
