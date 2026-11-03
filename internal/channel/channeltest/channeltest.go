// Package channeltest — тестовые данные модуля каналов: организации-владельцы и
// каналы в нужном состоянии, записанные прямо в базу.
package channeltest

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

// CategoryFood — категория из начального списка миграции 00004.
var CategoryFood = uuid.MustParse("0190a000-0000-7000-8000-000000000201")

var innSeq atomic.Int64

// Organization записывает организацию блогера (самозанятый) и возвращает её идентификатор.
// Таблица принадлежит модулю account: тест пишет строку напрямую, а не через его код.
func Organization(t testing.TB, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := ids.New()
	inn := fmt.Sprintf("7%011d", innSeq.Add(1)+time.Now().UnixNano()%1_000_000_000)
	_, err := pool.Exec(context.Background(), `INSERT INTO organization
		(id, legal_type, inn, name, contact_name, phone, tax_regime, vat_rate, status, created_at, updated_at)
		VALUES ($1, 'self_employed', $2, 'Блогер', 'Блогер', '+79990000000', 'npd', 'none', 'active', now(), now())`, id, inn)
	if err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	return id
}

// PriceAmount — цена блогера, которую Approve ставит формату f_1_24.
const PriceAmount = 300000

// Approve отмечает канал одобренным модерацией и активным в обход сценария S-16.
// Нужны права в порядке; категория и включённая цена f_1_24 ставятся, если их нет
// (без цены активный канал не фиксируется, R-3).
func Approve(t testing.TB, pool *pgxpool.Pool, channelID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `WITH price AS (
			INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
			VALUES ($3, $1, 'f_1_24', $4, true, now(), now())
			ON CONFLICT (channel_id, format_id) DO UPDATE SET is_enabled = true)
		UPDATE channel SET status = 'active', status_reason = NULL, ord_platform_ready_at = coalesce(ord_platform_ready_at, now()),
			category_id = coalesce(category_id, $2), moderated_at = now(), lock_version = lock_version + 1
		WHERE id = $1`, channelID, CategoryFood, ids.New(), PriceAmount)
	if err != nil {
		t.Fatalf("approve channel: %v", err)
	}
}
