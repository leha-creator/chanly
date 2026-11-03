// Package notificationtest — тестовые данные модуля уведомлений: организации, пользователи и
// администраторы, записанные прямо в базу (таблицы модуля account), и поддельные порты.
package notificationtest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/ids"
)

var seq atomic.Int64

// Organization записывает организацию (самозанятый) и её владельца; возвращает оба id и почту.
func Organization(t testing.TB, pool *pgxpool.Pool) (orgID, userID uuid.UUID, email string) {
	t.Helper()
	ctx := context.Background()
	orgID = ids.New()
	n := seq.Add(1) + time.Now().UnixNano()%1_000_000_000
	if _, err := pool.Exec(ctx, `INSERT INTO organization
		(id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, 'self_employed', $2, 'Организация', 'Контакт', '+79990000000', 'active', now(), now())`,
		orgID, fmt.Sprintf("6%011d", n%100_000_000_000)); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	userID, email = User(t, pool, "")
	if _, err := pool.Exec(ctx, `INSERT INTO organization_member (id, organization_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', now(), now())`, ids.New(), orgID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return orgID, userID, email
}

// User записывает пользователя; adminRole — пусто или moderator/senior.
func User(t testing.TB, pool *pgxpool.Pool, adminRole string) (uuid.UUID, string) {
	t.Helper()
	id := ids.New()
	email := fmt.Sprintf("u%d@notify.test", seq.Add(1)+time.Now().UnixNano()%1_000_000_000)
	var role *string
	if adminRole != "" {
		role = &adminRole
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO "user" (id, email, admin_role, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', now(), now())`, id, email, role); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id, email
}

// Channel записывает канал организации в модерации с пользователем MAX maxUser (0 — нельзя:
// поле обязательно, поэтому 0 записывается как есть, а переходник считает его «неизвестен»).
func Channel(t testing.TB, pool *pgxpool.Pool, orgID uuid.UUID, title string, maxUser int64) uuid.UUID {
	t.Helper()
	id := ids.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO channel (id, organization_id, max_chat_id, title, added_by_max_user_id,
		status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, 'moderation', now(), now())`,
		id, orgID, -8_000_000_000-seq.Add(1), title, maxUser); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	return id
}
