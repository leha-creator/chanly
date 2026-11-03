// Package ledgertest — тестовые данные журнала проводок: организации, пользователи и
// размещения, записанные прямо в базу. Таблицы принадлежат другим модулям: тест пишет
// строки напрямую, а не через их код.
package ledgertest

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var chatSeq atomic.Int64

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

// User записывает пользователя.
func User(t testing.TB, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := ids.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO "user" (id, email, status, created_at, updated_at)
		VALUES ($1, $2, 'active', now(), now())`, id, id.String()+"@ledger.test"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// Placement — размещение рекламодателя для проводок.
type Placement struct {
	ID            uuid.UUID
	VersionID     uuid.UUID
	OrderID       uuid.UUID
	ChannelID     uuid.UUID
	AdvertiserID  uuid.UUID
	BloggerID     uuid.UUID
	BloggerAmount money.Kopecks
	FeeAmount     money.Kopecks
	TotalAmount   money.Kopecks
}

// NewPlacement записывает кампанию, пост, версию, заказ, канал блогера и размещение в
// created с ценой блогера blogger и комиссией 15 %.
func NewPlacement(t testing.TB, pool *pgxpool.Pool, advertiser uuid.UUID, blogger money.Kopecks) Placement {
	t.Helper()
	ctx := context.Background()
	user := User(t, pool)
	bloggerOrg := Organization(t, pool, accountdomain.SelfEmployed, "Блогер")
	fee := blogger.ShareBP(1500)
	p := Placement{
		ID: ids.New(), VersionID: ids.New(), OrderID: ids.New(), ChannelID: ids.New(),
		AdvertiserID: advertiser, BloggerID: bloggerOrg, BloggerAmount: blogger, FeeAmount: fee, TotalAmount: blogger + fee,
	}
	campaign, post := ids.New(), ids.New()
	steps := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO campaign (id, organization_id, name, status, created_by, created_at, updated_at)
			VALUES ($1, $2, 'Кампания', 'draft', $3, now(), now())`, []any{campaign, advertiser, user}},
		{`INSERT INTO post (id, organization_id, campaign_id, status, draft, created_at, updated_at)
			VALUES ($1, $2, $3, 'draft', '{}', now(), now())`, []any{post, advertiser, campaign}},
		{`INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier, token_position,
			advertiser_name, advertiser_inn, created_by, created_at)
			VALUES ($1, $2, 1, 'pending_moderation', '{}', 'текст', 'text', 'below', 'Рекламодатель', '7707083893', $3, now())`,
			[]any{p.VersionID, post, user}},
		{`INSERT INTO "order" (id, number, organization_id, campaign_id, post_id, current_post_version_id, status,
			total_amount, created_by, created_at, updated_at)
			VALUES ($1, nextval('order_number_seq'), $2, $3, $4, $5, 'moderation', $6, $7, now(), now())`,
			[]any{p.OrderID, advertiser, campaign, post, p.VersionID, int64(p.TotalAmount), user}},
		{`INSERT INTO channel (id, organization_id, max_chat_id, added_by_max_user_id, status, is_public,
			category_id, bot_is_admin, bot_permissions, title, created_at, updated_at)
			VALUES ($1, $2, $3, 7, 'moderation', true, (SELECT id FROM channel_category ORDER BY position LIMIT 1), true,
			ARRAY['write', 'delete'], 'Канал блогера', now(), now())`, []any{p.ChannelID, bloggerOrg, -1000 - chatSeq.Add(1)}},
		{`INSERT INTO placement (id, order_id, channel_id, blogger_organization_id, format_id, top_hours, duration_hours,
			window_start, window_end, time_from, time_to, post_version_id, status, status_changed_at, fee_rate_bp,
			blogger_amount, fee_amount, total_amount, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'f_1_24', 1, 24, '2026-10-20 21:00:00+00', '2026-10-22 21:00:00+00', '09:00', '21:00',
			$5, 'created', now(), 1500, $6, $7, $8, now(), now())`,
			[]any{p.ID, p.OrderID, p.ChannelID, bloggerOrg, p.VersionID, int64(blogger), int64(fee), int64(p.TotalAmount)}},
	}
	for _, s := range steps {
		if _, err := pool.Exec(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("placement fixture: %v", err)
		}
	}
	return p
}

// Payout записывает попытку выплаты размещения (created, перевод вручную): основание проводки
// payout (ledger_transaction.payout_id ссылается на payout).
func Payout(t testing.TB, pool *pgxpool.Pool, p Placement, amount money.Kopecks) uuid.UUID {
	t.Helper()
	id := ids.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO payout (id, placement_id, blogger_organization_id, method,
		amount, attempt, status, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, 'bank_transfer', $4, 1, 'created', $5, now(), now())`,
		id, p.ID, p.BloggerID, int64(amount), "rw-payout:"+id.String()); err != nil {
		t.Fatalf("insert payout: %v", err)
	}
	return id
}
