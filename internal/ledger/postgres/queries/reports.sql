-- Чтение журнала: операции кабинета (R-9), сверка (R-15), распределения за период (R-16).

-- name: ListAccountEntries :many
-- Проводки счёта: кредит — со знаком «+», дебет — «−» (для пассива advertiser_balance).
SELECT e.id, e.amount, (e.credit_account_id = @account_id::uuid)::bool AS is_credit,
    t.id AS transaction_id, t.kind, t.placement_id, t.invoice_id, t.created_at
FROM ledger_entry e
JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
WHERE (e.debit_account_id = @account_id::uuid OR e.credit_account_id = @account_id::uuid)
  AND (NOT @has_cursor::bool OR (t.created_at, e.id) < (@cursor_at::timestamptz, @cursor_id::uuid))
ORDER BY t.created_at DESC, e.id DESC
LIMIT @lim;

-- name: SumBalancesBySide :one
-- Сумма кешей остатков активов и пассивов (06 §2.2 п. 3).
SELECT
    coalesce(sum(balance) FILTER (WHERE kind IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense')), 0)::bigint AS assets,
    coalesce(sum(balance) FILTER (WHERE kind NOT IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense')), 0)::bigint AS liabilities
FROM ledger_account;

-- name: ListCacheMismatches :many
-- Счета, у которых кеш остатка не равен сумме проводок (06 §2.2 п. 6).
WITH sums AS (
    SELECT a.id, a.kind, a.balance,
        ((CASE WHEN a.kind IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense') THEN 1 ELSE -1 END) * (
            coalesce((SELECT sum(amount) FROM ledger_entry WHERE debit_account_id = a.id), 0) -
            coalesce((SELECT sum(amount) FROM ledger_entry WHERE credit_account_id = a.id), 0)))::bigint AS computed
    FROM ledger_account a
)
SELECT id, kind, balance, computed FROM sums WHERE balance <> computed ORDER BY id;

-- name: ListHoldMismatches :many
-- Удержания, не совпадающие с остатком placement_hold: активное — остаток равен сумме,
-- без активного — остаток ноль (R-15 п. 3).
SELECT a.placement_id::uuid AS placement_id, a.balance, coalesce(h.amount, 0)::bigint AS held
FROM ledger_account a
LEFT JOIN hold h ON h.placement_id = a.placement_id AND h.status = 'active'
WHERE a.kind = 'placement_hold' AND a.balance <> coalesce(h.amount, 0)
UNION ALL
SELECT h.placement_id, 0::bigint, h.amount
FROM hold h
WHERE h.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM ledger_account a WHERE a.kind = 'placement_hold' AND a.placement_id = h.placement_id)
ORDER BY placement_id;

-- name: ListNegativeLiabilities :many
SELECT id, kind, balance FROM ledger_account
WHERE kind NOT IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense') AND balance < 0 ORDER BY id;

-- name: ListSettlements :many
-- Распределения удержаний за период [from, to): выплата блогеру и комиссия по размещению,
-- рекламодатель — из удержания (R-16).
SELECT t.placement_id::uuid AS placement_id, h.organization_id AS advertiser_id,
    coalesce(sum(e.amount) FILTER (WHERE c.kind = 'blogger_payable'), 0)::bigint AS payout,
    coalesce(sum(e.amount) FILTER (WHERE c.kind = 'platform_revenue'), 0)::bigint AS fee,
    t.created_at
FROM ledger_transaction t
JOIN ledger_entry e ON e.ledger_transaction_id = t.id
JOIN ledger_account c ON c.id = e.credit_account_id
JOIN hold h ON h.placement_id = t.placement_id AND h.status = 'distributed'
WHERE t.kind = 'settlement' AND t.created_at >= @from_at AND t.created_at < @to_at
GROUP BY t.id, t.placement_id, h.organization_id, t.created_at
ORDER BY h.organization_id, t.created_at, t.placement_id;

-- name: ListJournal :many
-- Журнал денег для служебной команды: проводки с видами счетов, новые сверху.
SELECT t.id AS transaction_id, t.kind, t.created_at, t.reason, e.amount,
    d.kind AS debit_kind, d.organization_id AS debit_org, d.placement_id AS debit_placement,
    c.kind AS credit_kind, c.organization_id AS credit_org, c.placement_id AS credit_placement
FROM ledger_entry e
JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
JOIN ledger_account d ON d.id = e.debit_account_id
JOIN ledger_account c ON c.id = e.credit_account_id
WHERE (NOT @has_org::bool OR d.organization_id = @org_id::uuid OR c.organization_id = @org_id::uuid)
  AND (@kind::text = '' OR t.kind = @kind::text)
  AND t.created_at >= @since::timestamptz
ORDER BY t.created_at DESC, t.id DESC, e.position
LIMIT @lim;

-- name: ListAdminEntries :many
-- Журнал денег администратора (adm/ledger): проводки с основаниями и вторым администратором
-- из журнала действий, новые сверху; смещение и общее число — CountAdminEntries.
SELECT t.id AS transaction_id, t.kind, t.created_at, t.reason, t.actor_type, t.actor_user_id,
    t.placement_id, t.invoice_id, t.payout_id, e.amount,
    d.kind AS debit_kind, d.organization_id AS debit_org, d.placement_id AS debit_placement,
    c.kind AS credit_kind, c.organization_id AS credit_org, c.placement_id AS credit_placement,
    -- Нулевой идентификатор — второго администратора не было.
    COALESCE((a.data ->> 'approver_id')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS approver_user_id
FROM ledger_entry e
JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
JOIN ledger_account d ON d.id = e.debit_account_id
JOIN ledger_account c ON c.id = e.credit_account_id
-- Ручные операции пишут одну запись журнала действий на проводку (ledger/app.adminPost).
LEFT JOIN audit_log a ON a.object_type = 'ledger_transaction' AND a.object_id = t.id::text AND a.data ? 'approver_id'
WHERE (sqlc.narg('org_id')::uuid IS NULL OR d.organization_id = sqlc.narg('org_id') OR c.organization_id = sqlc.narg('org_id'))
  AND (sqlc.narg('kind')::text IS NULL OR t.kind = sqlc.narg('kind'))
  AND (sqlc.narg('created_from')::timestamptz IS NULL OR t.created_at >= sqlc.narg('created_from'))
  AND (sqlc.narg('created_to')::timestamptz IS NULL OR t.created_at < sqlc.narg('created_to'))
ORDER BY t.created_at DESC, t.id DESC, e.position
LIMIT @lim OFFSET @off;

-- name: CountAdminEntries :one
SELECT count(*)::int
FROM ledger_entry e
JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
JOIN ledger_account d ON d.id = e.debit_account_id
JOIN ledger_account c ON c.id = e.credit_account_id
WHERE (sqlc.narg('org_id')::uuid IS NULL OR d.organization_id = sqlc.narg('org_id') OR c.organization_id = sqlc.narg('org_id'))
  AND (sqlc.narg('kind')::text IS NULL OR t.kind = sqlc.narg('kind'))
  AND (sqlc.narg('created_from')::timestamptz IS NULL OR t.created_at >= sqlc.narg('created_from'))
  AND (sqlc.narg('created_to')::timestamptz IS NULL OR t.created_at < sqlc.narg('created_to'));
