// Package placementtest — поддельные порты и тестовые данные модуля размещений: удержания
// в памяти с балансом (R-2), запись уведомлений (R-4), исполнитель Рокет Ворк (R-3), блогер
// с активным каналом и ценой.
package placementtest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Holds — удержания в памяти. Баланс организации без SetBalance не ограничен.
// Каждый вызов запоминает, была ли открыта транзакция (I-10).
type Holds struct {
	mu       sync.Mutex
	balance  map[uuid.UUID]money.Kopecks
	active   map[uuid.UUID]money.Kopecks
	owner    map[uuid.UUID]uuid.UUID
	released []uuid.UUID
	settled  []uuid.UUID
	settlers map[uuid.UUID]domain.Settler
	// OutsideTx — вызовы вне транзакции; должно быть 0.
	OutsideTx int
}

// NewHolds создаёт удержания без ограничений баланса.
func NewHolds() *Holds {
	return &Holds{balance: map[uuid.UUID]money.Kopecks{}, active: map[uuid.UUID]money.Kopecks{}, owner: map[uuid.UUID]uuid.UUID{}}
}

// SetBalance задаёт баланс организации: свободная сумма — баланс минус активные удержания.
func (h *Holds) SetBalance(orgID uuid.UUID, k money.Kopecks) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.balance[orgID] = k
}

func (h *Holds) checkTx(ctx context.Context) {
	if _, ok := tx.FromContext(ctx); !ok {
		h.OutsideTx++
	}
}

func (h *Holds) HoldAll(ctx context.Context, orgID uuid.UUID, reqs []domain.HoldRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkTx(ctx)
	var need money.Kopecks
	for _, r := range reqs {
		if _, ok := h.active[r.PlacementID]; !ok {
			need += r.Amount
		}
	}
	if b, limited := h.balance[orgID]; limited {
		var held money.Kopecks
		for id, amount := range h.active {
			if h.owner[id] == orgID {
				held += amount
			}
		}
		if free := b - held; need > free {
			return &domain.InsufficientBalanceError{Missing: need - free}
		}
	}
	for _, r := range reqs {
		if _, ok := h.active[r.PlacementID]; !ok {
			h.active[r.PlacementID], h.owner[r.PlacementID] = r.Amount, orgID
		}
	}
	return nil
}

func (h *Holds) Release(ctx context.Context, placementID uuid.UUID, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkTx(ctx)
	if _, ok := h.active[placementID]; ok {
		delete(h.active, placementID)
		h.released = append(h.released, placementID)
	}
	return nil
}

func (h *Holds) HasActive(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkTx(ctx)
	out := make(map[uuid.UUID]bool, len(placementIDs))
	for _, id := range placementIDs {
		_, out[id] = h.active[id]
	}
	return out, nil
}

func (h *Holds) Settle(ctx context.Context, placementID, _ uuid.UUID, d domain.Distribution, by domain.Settler) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkTx(ctx)
	amount, ok := h.active[placementID]
	if !ok {
		return fmt.Errorf("settle %s: no active hold", placementID)
	}
	if d.PayoutAmount+d.FeeCharged+d.RefundAmount != amount {
		return fmt.Errorf("settle %s: distribution does not match hold %d", placementID, amount)
	}
	delete(h.active, placementID)
	h.settled = append(h.settled, placementID)
	if h.settlers == nil {
		h.settlers = map[uuid.UUID]domain.Settler{}
	}
	h.settlers[placementID] = by
	return nil
}

// SettledBy — кто распределил удержание размещения (R-6 этапа приёмки); false — не распределено.
func (h *Holds) SettledBy(placementID uuid.UUID) (domain.Settler, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	by, ok := h.settlers[placementID]
	return by, ok
}

// Drop убирает активное удержание без снятия: нарушение I-03 для проверки R-7.
func (h *Holds) Drop(placementID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, placementID)
}

// Settled — размещения с распределённым удержанием.
func (h *Holds) Settled() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.settled...)
}

// Active — у размещения есть активное удержание.
func (h *Holds) Active(placementID uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.active[placementID]
	return ok
}

// ActiveCount — число активных удержаний.
func (h *Holds) ActiveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.active)
}

// Notifier — запись уведомлений.
type Notifier struct {
	mu   sync.Mutex
	list []domain.Notice
}

func (n *Notifier) Notify(_ context.Context, notice domain.Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.list = append(n.list, notice)
	return nil
}

// Templates — шаблоны уведомлений по порядку.
func (n *Notifier) Templates() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.list))
	for _, x := range n.list {
		out = append(out, x.Template+":"+string(x.Role))
	}
	return out
}

// Count — число уведомлений шаблона.
func (n *Notifier) Count(template string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, x := range n.list {
		if x.Template == template {
			c++
		}
	}
	return c
}

// Last — последнее уведомление шаблона и роли.
func (n *Notifier) Last(template string, role domain.ActorType) (domain.Notice, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := len(n.list) - 1; i >= 0; i-- {
		if n.list[i].Template == template && n.list[i].Role == role {
			return n.list[i], true
		}
	}
	return domain.Notice{}, false
}

// Contractors — исполнитель Рокет Ворк: Deny запрещает принятие организации.
type Contractors struct {
	mu   sync.Mutex
	deny map[uuid.UUID]bool
}

// Deny запрещает или разрешает принятие организацией-блогером.
func (c *Contractors) Deny(orgID uuid.UUID, deny bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deny == nil {
		c.deny = map[uuid.UUID]bool{}
	}
	c.deny[orgID] = deny
}

func (c *Contractors) CanAccept(_ context.Context, orgID uuid.UUID) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.deny[orgID], nil
}

// Payouts — поддельное создание выплаты: запоминает запросы (Created).
type Payouts struct {
	mu      sync.Mutex
	created []domain.PayoutRequest
}

func (p *Payouts) Create(_ context.Context, r domain.PayoutRequest) (uuid.UUID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created = append(p.created, r)
	return uuid.New(), nil
}

// Created — запросы на выплату по порядку.
func (p *Payouts) Created() []domain.PayoutRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.PayoutRequest(nil), p.created...)
}

var seq atomic.Int64

// Blogger записывает организацию блогера (самозанятый) и её пользователя. Таблицы
// принадлежат модулю account: тест пишет строки напрямую.
func Blogger(t testing.TB, pool *pgxpool.Pool) (orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	orgID, userID = ids.New(), ids.New()
	n := seq.Add(1) + time.Now().UnixNano()%1_000_000_000
	if _, err := pool.Exec(ctx, `INSERT INTO organization
		(id, legal_type, inn, name, contact_name, phone, tax_regime, vat_rate, status, created_at, updated_at)
		VALUES ($1, 'self_employed', $2, 'Блогер', 'Блогер', '+79990000000', 'npd', 'none', 'active', now(), now())`,
		orgID, fmt.Sprintf("5%011d", n%100_000_000_000)); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, status, created_at, updated_at)
		VALUES ($1, $2, 'active', now(), now())`, userID, fmt.Sprintf("blg%d@example.test", n)); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_member (id, organization_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', now(), now())`, ids.New(), orgID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return orgID, userID
}

// CategoryFood — категория из начального списка миграции 00004.
var CategoryFood = uuid.MustParse("0190a000-0000-7000-8000-000000000201")

// Channel записывает активный канал блогера с ценой формата f_1_24 (amount — цена блогера)
// в обход сценариев модуля каналов.
func Channel(t testing.TB, pool *pgxpool.Pool, ownerID uuid.UUID, title string, amount money.Kopecks) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := ids.New()
	if _, err := pool.Exec(ctx, `INSERT INTO channel (id, organization_id, max_chat_id, title, link, added_by_max_user_id, status,
		is_public, category_id, bot_is_admin, bot_permissions, subscribers_count, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'https://max.ru/test', 7, 'moderation', true, $5, true, ARRAY['write', 'delete'], 5000, now(), now())`,
		id, ownerID, -9_000_000_000-seq.Add(1), title, CategoryFood); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `WITH price AS (
			INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
			VALUES ($2, $1, 'f_1_24', $3, true, now(), now()))
		UPDATE channel SET status = 'active', moderated_at = now(), ord_platform_ready_at = now(), lock_version = lock_version + 1 WHERE id = $1`,
		id, ids.New(), int64(amount)); err != nil {
		t.Fatalf("activate channel: %v", err)
	}
	return id
}

// SetPrice меняет цену блогера формата f_1_24 канала.
func SetPrice(t testing.TB, pool *pgxpool.Pool, channelID uuid.UUID, amount money.Kopecks) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE channel_price SET amount = $2 WHERE channel_id = $1 AND format_id = 'f_1_24'`,
		channelID, int64(amount)); err != nil {
		t.Fatalf("set price: %v", err)
	}
}
