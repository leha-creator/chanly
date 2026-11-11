package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/postgres/sqlc"
)

type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Store { return &Store{pool: pool, log: log} }
func (s *Store) q(ctx context.Context) *sqlc.Queries  { return sqlc.New(db.Conn(ctx, s.pool)) }
func uid(id string) (pgtype.UUID, error)              { var u pgtype.UUID; err := u.Scan(id); return u, err }
func uuidString(u pgtype.UUID) string                 { return uuid.UUID(u.Bytes).String() }

func (s *Store) Run(ctx context.Context, id string) (domain.Run, error) {
	u, err := uid(id)
	if err != nil {
		return domain.Run{}, domain.ErrDenied
	}
	r, err := s.q(ctx).GetRun(ctx, u)
	if err != nil {
		return domain.Run{}, err
	}
	result := domain.Run{ID: uuidString(r.ID), AccountID: r.AccountID, GrantID: uuidString(r.GrantID), ExternalID: r.ExternalID, ManifestHash: r.ManifestHash, Manifest: r.Manifest, TaskID: r.TaskID, Nominal: money.Kopecks(r.Nominal), FeeCeiling: money.Kopecks(r.FeeCeiling), State: r.State}
	if r.ActiveGrantID.Valid {
		result.GrantID = uuidString(r.ActiveGrantID)
	}
	if r.ProviderPaidAt.Valid {
		result.ProviderPaidAt = &r.ProviderPaidAt.Time
	}
	if r.ReservedAt.Valid {
		result.ReservedAt = &r.ReservedAt.Time
	}
	if r.SettledAt.Valid {
		result.SettledAt = &r.SettledAt.Time
	}
	if r.ActualNominal != nil {
		n := money.Kopecks(*r.ActualNominal)
		result.ActualNominal = &n
	}
	if r.ActualFee != nil {
		n := money.Kopecks(*r.ActualFee)
		result.ActualFee = &n
	}
	if r.LedgerID.Valid {
		id := uuidString(r.LedgerID)
		result.LedgerID = &id
	}
	return result, nil
}

func (s *Store) Grant(ctx context.Context, id string) (domain.Grant, error) {
	u, err := uid(id)
	if err != nil {
		return domain.Grant{}, domain.ErrDenied
	}
	g, err := s.q(ctx).GetGrant(ctx, u)
	if err != nil {
		return domain.Grant{}, err
	}
	var p domain.Policy
	if json.Unmarshal(g.Policy, &p) != nil || p.Validate() != nil || p.AccountID != g.AccountID || p.Version != g.Version {
		return domain.Grant{}, domain.ErrDenied
	}
	p.NotBefore = g.NotBefore.Time
	p.ExpiresAt = g.ExpiresAt.Time
	p.Revoked = g.RevokedAt.Valid
	a, err := s.q(ctx).GetAccount(ctx, g.AccountID)
	if err != nil {
		return domain.Grant{}, err
	}
	if p.CompanyINN != a.CompanyInn {
		return domain.Grant{}, domain.ErrDenied
	}
	p.Killed = a.Killed
	return domain.Grant{ID: id, Policy: p, IssuedBy: g.IssuedBy}, nil
}

func (s *Store) Prepare(ctx context.Context, r domain.Run) (domain.Run, error) {
	u, err := uid(r.ID)
	if err != nil {
		return domain.Run{}, domain.ErrDenied
	}
	g, err := uid(r.GrantID)
	if err != nil {
		return domain.Run{}, domain.ErrDenied
	}
	_, err = s.q(ctx).PrepareRun(ctx, sqlc.PrepareRunParams{ID: u, GrantID: g, ManifestHash: r.ManifestHash, Manifest: r.Manifest, ExternalID: r.ExternalID, Nominal: int64(r.Nominal), FeeCeiling: int64(r.FeeCeiling)})
	if err != nil {
		s.log.WarnContext(ctx, "diagnostic prepare denied", "run", r.ID)
		return domain.Run{}, err
	}
	s.log.InfoContext(ctx, "diagnostic run prepared", "run", r.ID, "grant", r.GrantID)
	return s.Run(ctx, r.ID)
}

func (s *Store) Checkpoint(ctx context.Context, id string, a domain.Action) (domain.Checkpoint, error) {
	u, err := uid(id)
	if err != nil {
		return domain.Checkpoint{}, domain.ErrDenied
	}
	c, err := s.q(ctx).GetCheckpoint(ctx, sqlc.GetCheckpointParams{RunID: u, Action: string(a)})
	return domain.Checkpoint{RunID: id, Action: a, State: c.State, RequestHash: c.RequestHash}, err
}

var _ domain.ControlStore = (*Store)(nil)

func (s *Store) RecordPaidAt(ctx context.Context, id string, at time.Time) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).RecordProviderPaidAt(ctx, sqlc.RecordProviderPaidAtParams{RunID: u, PaidAt: pgtype.Timestamptz{Time: at, Valid: true}}); err != nil {
		return domain.ErrFinancialMismatch
	}
	return nil
}

func (s *Store) Release(ctx context.Context, id string) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).ReleaseRun(ctx, u); err != nil {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic zero-charge hold released", "run", id)
	return nil
}

func (s *Store) Freeze(ctx context.Context, id string) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).FreezeRun(ctx, u); err != nil {
		return domain.ErrDenied
	}
	s.log.ErrorContext(ctx, "diagnostic account frozen", "run", id)
	return nil
}

func (s *Store) EventOwner(ctx context.Context, alias, external string, task int64) (string, error) {
	id, err := s.q(ctx).DiagnosticEventOwner(ctx, sqlc.DiagnosticEventOwnerParams{Alias: alias, ExternalID: external, TaskID: task})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", domain.ErrDenied
	}
	return uuidString(id), nil
}

func (s *Store) Settle(ctx context.Context, id string, nominal, fee money.Kopecks, ledger string) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	l, err := uid(ledger)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).SettleRun(ctx, sqlc.SettleRunParams{RunID: u, Nominal: int64(nominal), Fee: int64(fee), LedgerID: l}); err != nil {
		return domain.ErrDenied
	}
	return nil
}

func (s *Store) Now(ctx context.Context) (time.Time, error) {
	t, err := s.q(ctx).ControlTime(ctx)
	return t.Time, err
}

func (s *Store) Intent(ctx context.Context, id string, a domain.Action, hash string) (bool, error) {
	u, err := uid(id)
	if err != nil {
		return false, domain.ErrDenied
	}
	ok, err := s.q(ctx).CommitIntent(ctx, sqlc.CommitIntentParams{RunID: u, Action: string(a), RequestHash: hash})
	if err != nil {
		s.log.WarnContext(ctx, "diagnostic intent denied", "run", id, "action", a)
		return false, domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic intent checked", "run", id, "action", a, "new_intent", ok)
	return ok, nil
}

func (s *Store) Observe(ctx context.Context, id string, a domain.Action, state string, taskID *int64) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	err = s.q(ctx).ObserveAction(ctx, sqlc.ObserveActionParams{RunID: u, Action: string(a), State: state, TaskID: taskID})
	if err != nil {
		s.log.ErrorContext(ctx, "diagnostic observation rejected", "run", id, "action", a)
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic action observed", "run", id, "action", a, "state", state)
	return nil
}
