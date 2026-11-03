// Package posttest — тестовые данные модуля post: организация рекламодателя с
// пользователем, записанные прямо в базу.
package posttest

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

// Name — название организации рекламодателя из Advertiser.
const Name = "ООО Ромашка"

// Advertiser записывает организацию рекламодателя (юрлицо) и её участника и
// возвращает идентификаторы организации и пользователя. Таблицы принадлежат модулю
// account: тест пишет строки напрямую, а не через его код.
func Advertiser(t testing.TB, pool *pgxpool.Pool) (orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	orgID, userID = ids.New(), ids.New()
	n := seq.Add(1) + time.Now().UnixNano()%1_000_000_000
	inn := fmt.Sprintf("77%08d", n%100_000_000)
	if _, err := pool.Exec(ctx, `INSERT INTO organization
		(id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, 'legal_entity', $2, $3, 'Иван', '+79990000000', 'active', now(), now())`, orgID, inn, Name); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, status, created_at, updated_at)
		VALUES ($1, $2, 'active', now(), now())`, userID, fmt.Sprintf("adv%d@example.test", n)); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_member (id, organization_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', now(), now())`, ids.New(), orgID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO advertiser_profile (organization_id, created_at, updated_at)
		VALUES ($1, now(), now())`, orgID); err != nil {
		t.Fatalf("insert advertiser profile: %v", err)
	}
	return orgID, userID
}
