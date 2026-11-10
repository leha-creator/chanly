// Package postgres реализует хранилище модуля выплат на PostgreSQL: исполнители Рокет Ворк
// и попытки выплат. Запросы — в queries/*.sql, код доступа генерирует sqlc. Методы работают
// в транзакции из context.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Store — хранилище модуля.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ domain.Store = (*Store)(nil)

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store { return &Store{pool: pool, log: log} }

func (s *Store) q(ctx context.Context) *sqlc.Queries { return sqlc.New(db.Conn(ctx, s.pool)) }

// fail переводит ошибку базы: «нет строк» — ErrNotFound, вторая живая выплата — ErrPayoutExists;
// остальное пишется в журнал как непредвиденное.
func (s *Store) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case db.UniqueViolation(err, "payout_placement_live_key"), db.UniqueViolation(err, "payout_attempt_key"):
		return domain.ErrPayoutExists
	}
	if !errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "repository call failed", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	return err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidOf(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ---------------------------------------------------------------- исполнители

func contractorOf(r sqlc.RocketworkContractor) domain.Contractor {
	return domain.Contractor{
		ID: r.ID, OrganizationID: r.OrganizationID, ExternalID: str(r.ExternalID), Status: domain.ContractorStatus(r.Status),
		BlockedReason: str(r.BlockedReason), OnboardingURL: str(r.OnboardingUrl), StatusCheckedAt: utcPtr(r.StatusCheckedAt),
		LastError: str(r.LastError), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (s *Store) ContractorByOrg(ctx context.Context, orgID uuid.UUID, forUpdate bool) (domain.Contractor, error) {
	const op = "Payout.ContractorByOrg"
	q := s.q(ctx)
	get := q.GetContractorByOrg
	if forUpdate {
		get = q.GetContractorByOrgForUpdate
	}
	r, err := get(ctx, orgID)
	if err != nil {
		return domain.Contractor{}, s.fail(ctx, op, err)
	}
	return contractorOf(r), nil
}

func (s *Store) ContractorByID(ctx context.Context, id uuid.UUID, forUpdate bool) (domain.Contractor, error) {
	const op = "Payout.ContractorByID"
	q := s.q(ctx)
	get := q.GetContractor
	if forUpdate {
		get = q.GetContractorForUpdate
	}
	r, err := get(ctx, id)
	if err != nil {
		return domain.Contractor{}, s.fail(ctx, op, err)
	}
	return contractorOf(r), nil
}

func (s *Store) ContractorByExternalID(ctx context.Context, externalID string, forUpdate bool) (domain.Contractor, error) {
	const op = "Payout.ContractorByExternalID"
	q := s.q(ctx)
	get := q.GetContractorByExternalID
	if forUpdate {
		get = q.GetContractorByExternalIDForUpdate
	}
	r, err := get(ctx, &externalID)
	if err != nil {
		return domain.Contractor{}, s.fail(ctx, op, err)
	}
	return contractorOf(r), nil
}

func (s *Store) InsertContractor(ctx context.Context, c domain.Contractor) (domain.Contractor, bool, error) {
	const op = "Payout.InsertContractor"
	n, err := s.q(ctx).InsertContractor(ctx, sqlc.InsertContractorParams{
		ID: c.ID, OrganizationID: c.OrganizationID, ExternalID: strPtr(c.ExternalID), Status: string(c.Status),
		BlockedReason: strPtr(c.BlockedReason), OnboardingUrl: strPtr(c.OnboardingURL), StatusCheckedAt: c.StatusCheckedAt,
		LastError: strPtr(c.LastError), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return domain.Contractor{}, false, s.fail(ctx, op, err)
	}
	if n == 1 {
		return c, true, nil
	}
	// Другой запрос уже создал строку организации: читаем её под блокировкой.
	s.log.DebugContext(ctx, "payout store", logx.KeyOp, op, "conflict", "organization_id", "organization_id", c.OrganizationID)
	existing, err := s.ContractorByOrg(ctx, c.OrganizationID, true)
	return existing, false, err
}

func (s *Store) UpdateContractor(ctx context.Context, c domain.Contractor) error {
	const op = "Payout.UpdateContractor"
	n, err := s.q(ctx).UpdateContractor(ctx, sqlc.UpdateContractorParams{
		ID: c.ID, ExternalID: strPtr(c.ExternalID), Status: string(c.Status), BlockedReason: strPtr(c.BlockedReason),
		OnboardingUrl: strPtr(c.OnboardingURL), StatusCheckedAt: c.StatusCheckedAt, LastError: strPtr(c.LastError),
		UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return s.fail(ctx, op, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ContractorCounts(ctx context.Context) (map[domain.ContractorStatus]int, error) {
	rows, err := s.q(ctx).CountContractorsByStatus(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Payout.ContractorCounts", err)
	}
	out := make(map[domain.ContractorStatus]int, len(rows))
	for _, r := range rows {
		out[domain.ContractorStatus(r.Status)] = int(r.N)
	}
	return out, nil
}

// ---------------------------------------------------------------- выплаты

func payoutOf(r sqlc.Payout) domain.Payout {
	p := domain.Payout{
		ReconciliationRequired: r.ReconciliationRequired,
		ID:                     r.ID, PlacementID: r.PlacementID, BloggerOrganizationID: r.BloggerOrganizationID, Method: domain.Method(r.Method),
		ContractorID: uuidOf(r.RocketworkContractorID), Amount: money.Kopecks(r.Amount), Attempt: int(r.Attempt),
		Status: domain.Status(r.Status), ExternalID: str(r.ExternalID), ReceiptURL: str(r.ReceiptUrl),
		BankReference: str(r.BankReference), FailureCode: domain.FailureCode(str(r.FailureCode)),
		FailureMessage: str(r.FailureMessage), SentAt: utcPtr(r.SentAt), CompletedAt: utcPtr(r.CompletedAt),
		IdempotencyKey: r.IdempotencyKey, LockVersion: int(r.LockVersion), CreatedBy: uuidOf(r.CreatedBy),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.ProviderFee != nil {
		p.ProviderFee = money.Kopecks(*r.ProviderFee)
	}
	return p
}

// fee — комиссия известна только у succeeded.
func fee(p domain.Payout) *int64 {
	if p.Status != domain.StatusSucceeded {
		return nil
	}
	v := int64(p.ProviderFee)
	return &v
}

func (s *Store) InsertPayout(ctx context.Context, p domain.Payout) error {
	const op = "Payout.InsertPayout"
	err := s.q(ctx).InsertPayout(ctx, sqlc.InsertPayoutParams{
		ID: p.ID, PlacementID: p.PlacementID, BloggerOrganizationID: p.BloggerOrganizationID, Method: string(p.Method),
		RocketworkContractorID: uuidPtr(p.ContractorID), Amount: int64(p.Amount), Attempt: int32(p.Attempt),
		Status: string(p.Status), ExternalID: strPtr(p.ExternalID), ProviderFee: fee(p), ReceiptUrl: strPtr(p.ReceiptURL),
		BankReference: strPtr(p.BankReference), FailureCode: strPtr(string(p.FailureCode)),
		FailureMessage: strPtr(p.FailureMessage), SentAt: p.SentAt, CompletedAt: p.CompletedAt,
		IdempotencyKey: p.IdempotencyKey, CreatedBy: uuidPtr(p.CreatedBy), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	})
	if err != nil {
		return s.fail(ctx, op, err)
	}
	return nil
}

func (s *Store) PayoutByID(ctx context.Context, id uuid.UUID, forUpdate bool) (domain.Payout, error) {
	q := s.q(ctx)
	get := q.GetPayout
	if forUpdate {
		get = q.GetPayoutForUpdate
	}
	r, err := get(ctx, id)
	if err != nil {
		return domain.Payout{}, s.fail(ctx, "Payout.PayoutByID", err)
	}
	return payoutOf(r), nil
}

func (s *Store) PayoutByExternalID(ctx context.Context, externalID string, forUpdate bool) (domain.Payout, error) {
	q := s.q(ctx)
	get := q.GetPayoutByExternalID
	if forUpdate {
		get = q.GetPayoutByExternalIDForUpdate
	}
	r, err := get(ctx, &externalID)
	if err != nil {
		return domain.Payout{}, s.fail(ctx, "Payout.PayoutByExternalID", err)
	}
	return payoutOf(r), nil
}

func (s *Store) LatestPayout(ctx context.Context, placementID uuid.UUID, forUpdate bool) (domain.Payout, error) {
	q := s.q(ctx)
	get := q.GetLatestPayout
	if forUpdate {
		get = q.GetLatestPayoutForUpdate
	}
	r, err := get(ctx, placementID)
	if err != nil {
		return domain.Payout{}, s.fail(ctx, "Payout.LatestPayout", err)
	}
	return payoutOf(r), nil
}

func (s *Store) UpdatePayout(ctx context.Context, p *domain.Payout) error {
	const op = "Payout.UpdatePayout"
	n, err := s.q(ctx).UpdatePayout(ctx, sqlc.UpdatePayoutParams{
		ID: p.ID, LockVersion: int32(p.LockVersion), Status: string(p.Status), ExternalID: strPtr(p.ExternalID),
		ProviderFee: fee(*p), ReceiptUrl: strPtr(p.ReceiptURL), BankReference: strPtr(p.BankReference),
		FailureCode: strPtr(string(p.FailureCode)), FailureMessage: strPtr(p.FailureMessage), SentAt: p.SentAt,
		CompletedAt: p.CompletedAt, UpdatedAt: p.UpdatedAt,
	})
	if err != nil {
		return s.fail(ctx, op, err)
	}
	if n == 0 {
		s.log.DebugContext(ctx, "payout store", logx.KeyOp, op, "conflict", "lock_version", "payout_id", p.ID,
			"lock_version", p.LockVersion)
		return fmt.Errorf("%w: payout %s", domain.ErrConcurrentUpdate, p.ID)
	}
	p.LockVersion++
	return nil
}

func (s *Store) List(ctx context.Context, orgID uuid.UUID, after *domain.Cursor, limit int) ([]domain.Payout, error) {
	arg := sqlc.ListPayoutsParams{OrganizationID: orgID, Lim: int32(limit)}
	if after != nil {
		arg.HasCursor, arg.CursorAt, arg.CursorID = true, after.At, after.ID
	}
	rows, err := s.q(ctx).ListPayouts(ctx, arg)
	if err != nil {
		return nil, s.fail(ctx, "Payout.List", err)
	}
	return payoutsOf(rows), nil
}

func payoutsOf(rows []sqlc.Payout) []domain.Payout {
	out := make([]domain.Payout, 0, len(rows))
	for _, r := range rows {
		out = append(out, payoutOf(r))
	}
	return out
}

func (s *Store) Summary(ctx context.Context, orgID uuid.UUID) (domain.Summary, error) {
	r, err := s.q(ctx).PayoutSummary(ctx, orgID)
	if err != nil {
		return domain.Summary{}, s.fail(ctx, "Payout.Summary", err)
	}
	return domain.Summary{Received: money.Kopecks(r.Received), InProgress: money.Kopecks(r.InProgress), Count: int(r.N)}, nil
}

func (s *Store) SucceededSum(ctx context.Context, orgID uuid.UUID, from, to time.Time) (money.Kopecks, error) {
	total, err := s.q(ctx).SucceededSum(ctx, sqlc.SucceededSumParams{OrganizationID: orgID, FromAt: &from, ToAt: &to})
	if err != nil {
		return 0, s.fail(ctx, "Payout.SucceededSum", err)
	}
	return money.Kopecks(total), nil
}

func (s *Store) AdminList(ctx context.Context, f domain.AdminFilter) ([]domain.Payout, error) {
	limit := f.Limit
	if limit <= 0 || limit > 10000 { // выгрузка CSV — до 10 000 строк
		limit = 100
	}
	arg := sqlc.AdminListPayoutsParams{Status: string(f.Status), Lim: int32(limit)}
	if f.OrganizationID != uuid.Nil {
		arg.HasOrg, arg.OrganizationID = true, f.OrganizationID
	}
	if !f.From.IsZero() {
		arg.HasFrom, arg.FromAt = true, f.From
	}
	if !f.To.IsZero() {
		arg.HasTo, arg.ToAt = true, f.To
	}
	rows, err := s.q(ctx).AdminListPayouts(ctx, arg)
	if err != nil {
		return nil, s.fail(ctx, "Payout.AdminList", err)
	}
	return payoutsOf(rows), nil
}

// ---------------------------------------------------------------- депозит и зависшие

func (s *Store) RocketworkPayable(ctx context.Context) (money.Kopecks, error) {
	v, err := s.q(ctx).RocketworkPayable(ctx)
	if err != nil {
		return 0, s.fail(ctx, "Payout.RocketworkPayable", err)
	}
	return money.Kopecks(v), nil
}

func (s *Store) RocketworkInFlight(ctx context.Context) (money.Kopecks, error) {
	v, err := s.q(ctx).RocketworkInFlight(ctx)
	if err != nil {
		return 0, s.fail(ctx, "Payout.RocketworkInFlight", err)
	}
	return money.Kopecks(v), nil
}

func (s *Store) AcceptedUnpaid(ctx context.Context, before time.Time) ([]domain.StuckItem, error) {
	rows, err := s.q(ctx).AcceptedUnpaid(ctx, before)
	if err != nil {
		return nil, s.fail(ctx, "Payout.AcceptedUnpaid", err)
	}
	out := make([]domain.StuckItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.StuckItem{ObjectKey: "placement:" + r.ID.String(), Since: r.StatusChangedAt.UTC()})
	}
	return out, nil
}

func (s *Store) PayoutsInStatusBefore(ctx context.Context, status domain.Status, method domain.Method, before time.Time) ([]domain.StuckItem, error) {
	rows, err := s.q(ctx).PayoutsInStatusBefore(ctx, sqlc.PayoutsInStatusBeforeParams{
		Status: string(status), Method: string(method), Before: before})
	if err != nil {
		return nil, s.fail(ctx, "Payout.PayoutsInStatusBefore", err)
	}
	out := make([]domain.StuckItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.StuckItem{ObjectKey: "payout:" + r.ID.String(), Since: r.UpdatedAt.UTC()})
	}
	return out, nil
}

// ---------------------------------------------------------------- реестр администратора

func (s *Store) AdminPage(ctx context.Context, f domain.AdminFilter) ([]domain.Payout, int, error) {
	arg := sqlc.AdminPagePayoutsParams{Status: string(f.Status), Lim: int32(f.Limit), Off: int32(f.Offset)}
	cnt := sqlc.AdminCountPayoutsParams{Status: string(f.Status)}
	if f.OrganizationID != uuid.Nil {
		arg.HasOrg, arg.OrganizationID = true, f.OrganizationID
		cnt.HasOrg, cnt.OrganizationID = true, f.OrganizationID
	}
	if !f.From.IsZero() {
		arg.HasFrom, arg.FromAt = true, f.From
		cnt.HasFrom, cnt.FromAt = true, f.From
	}
	if !f.To.IsZero() {
		arg.HasTo, arg.ToAt = true, f.To
		cnt.HasTo, cnt.ToAt = true, f.To
	}
	q := s.q(ctx)
	rows, err := q.AdminPagePayouts(ctx, arg)
	if err != nil {
		return nil, 0, s.fail(ctx, "Payout.AdminPage", err)
	}
	total, err := q.AdminCountPayouts(ctx, cnt)
	if err != nil {
		return nil, 0, s.fail(ctx, "Payout.AdminCount", err)
	}
	return payoutsOf(rows), int(total), nil
}

func (s *Store) AdminTotals(ctx context.Context) ([]domain.StatusTotal, error) {
	rows, err := s.q(ctx).AdminPayoutTotals(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Payout.AdminTotals", err)
	}
	out := make([]domain.StatusTotal, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.StatusTotal{Status: domain.Status(r.Status), Count: int(r.N), Amount: money.Kopecks(r.Amount)})
	}
	return out, nil
}

func (s *Store) PlacementPayouts(ctx context.Context, placementID uuid.UUID) ([]domain.Payout, error) {
	rows, err := s.q(ctx).ListPlacementPayouts(ctx, placementID)
	if err != nil {
		return nil, s.fail(ctx, "Payout.PlacementPayouts", err)
	}
	return payoutsOf(rows), nil
}
