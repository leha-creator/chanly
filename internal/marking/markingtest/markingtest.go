// Package markingtest — тестовые данные модуля маркировки: оферты и их принятие, организации
// с реквизитами, записанные прямо в базу. Таблицы принадлежат модулю account: тест пишет
// строки напрямую, а не через его код.
package markingtest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// Виды оферт (04 §2).
const (
	AdvertiserOffer = "advertiser_offer"
	BloggerOffer    = "blogger_offer"
)

// Platform — реквизиты платформы для EnsurePlatformOrganization в тестах.
var (
	PlatformINN  = accounttest.INN(accountdomain.SoleProprietor)
	PlatformName = "ИП Платформа"
)

// Offer публикует версию оферты вида kind (если её ещё нет) и возвращает её идентификатор.
func Offer(t testing.TB, pool *pgxpool.Pool, kind string, version int) uuid.UUID {
	t.Helper()
	id := ids.New()
	var got uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO legal_document (id, kind, version, published_at, storage_key, sha256, created_at)
		VALUES ($1, $2, $3, now(), 'legal/' || $2 || '.pdf', 'sha', now())
		ON CONFLICT (kind, version) DO UPDATE SET kind = EXCLUDED.kind RETURNING id`, id, kind, version).Scan(&got); err != nil {
		t.Fatalf("insert legal document: %v", err)
	}
	return got
}

// Accept записывает принятие оферты kind версии 1 организацией в момент at.
func Accept(t testing.TB, pool *pgxpool.Pool, orgID uuid.UUID, kind string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	doc := Offer(t, pool, kind, 1)
	user := ids.New()
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, status, created_at, updated_at)
		VALUES ($1, $2, 'active', now(), now())`, user, user.String()+"@offer.test"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO legal_acceptance (id, legal_document_id, user_id, organization_id, accepted_at, created_at)
		VALUES ($1, $2, $3, $4, $5, now())`, ids.New(), doc, user, orgID, at); err != nil {
		t.Fatalf("insert legal acceptance: %v", err)
	}
}

// Organization записывает организацию с корректным ИНН для типа лица.
func Organization(t testing.TB, pool *pgxpool.Pool, legalType accountdomain.LegalType, name string) uuid.UUID {
	t.Helper()
	id := ids.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO organization
		(id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'Контакт', '+79990000000', 'active', now(), now())`,
		id, string(legalType), accounttest.INN(legalType), name); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	return id
}
