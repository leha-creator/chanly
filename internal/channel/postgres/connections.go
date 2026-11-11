package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
)

type Connections struct{ s *Store }

func (s *Store) Connections() *Connections { return &Connections{s} }

var _ domain.Connections = (*Connections)(nil)

func (r *Connections) DeleteExpired(ctx context.Context, before time.Time, limit int32) (int64, error) {
	n, err := r.s.q(ctx).DeleteExpiredChannelConnections(ctx, sqlc.DeleteExpiredChannelConnectionsParams{Before: before, Lim: limit})
	return n, r.result(ctx, "Channel.Connection.DeleteExpired", err)
}

func (r *Connections) result(ctx context.Context, op string, err error) error {
	r.s.debug(ctx, op)
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Connections) Create(ctx context.Context, c *domain.Connection) error {
	return r.result(ctx, "Channel.Connection.Create", r.s.q(ctx).InsertChannelConnection(ctx, sqlc.InsertChannelConnectionParams{
		ID: c.ID, OrganizationID: c.OrganizationID, UserID: c.UserID, TokenHash: c.TokenHash,
		ExpiresAt: c.ExpiresAt, Status: c.Status, ScanID: c.ScanID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}))
}

func connection(row sqlc.ChannelConnection) *domain.Connection {
	return &domain.Connection{ID: row.ID, OrganizationID: row.OrganizationID, UserID: row.UserID,
		TokenHash: row.TokenHash, MaxUserID: row.MaxUserID, TokenUsedAt: row.TokenUsedAt, ExpiresAt: row.ExpiresAt,
		Status: row.Status, Reason: row.Reason, ScanID: row.ScanID, ChannelID: row.ChannelID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (r *Connections) Get(ctx context.Context, id uuid.UUID, lock bool) (*domain.Connection, error) {
	var row sqlc.ChannelConnection
	var err error
	if lock {
		row, err = r.s.q(ctx).GetChannelConnectionForUpdate(ctx, id)
	} else {
		row, err = r.s.q(ctx).GetChannelConnection(ctx, id)
	}
	if err = r.result(ctx, "Channel.Connection.Get", err); err != nil {
		return nil, err
	}
	return connection(row), nil
}

func (r *Connections) FindTokenForUpdate(ctx context.Context, hash string) (*domain.Connection, error) {
	row, err := r.s.q(ctx).FindChannelConnectionTokenForUpdate(ctx, hash)
	if err = r.result(ctx, "Channel.Connection.FindToken", err); err != nil {
		return nil, err
	}
	return connection(row), nil
}

func (r *Connections) Save(ctx context.Context, c *domain.Connection) error {
	return r.result(ctx, "Channel.Connection.Save", r.s.q(ctx).SaveChannelConnection(ctx, sqlc.SaveChannelConnectionParams{
		ID: c.ID, MaxUserID: c.MaxUserID, TokenUsedAt: c.TokenUsedAt, Status: c.Status, Reason: c.Reason,
		ScanID: c.ScanID, ChannelID: c.ChannelID, UpdatedAt: c.UpdatedAt,
	}))
}

func (r *Connections) CountSince(ctx context.Context, org uuid.UUID, since time.Time) (int, error) {
	if err := r.s.q(ctx).LockChannelConnectionIssuance(ctx, org.String()); err != nil {
		return 0, r.result(ctx, "Channel.Connection.LockIssuance", err)
	}
	n, err := r.s.q(ctx).CountChannelConnectionsSince(ctx, sqlc.CountChannelConnectionsSinceParams{OrganizationID: org, Since: since})
	return int(n), r.result(ctx, "Channel.Connection.Count", err)
}

func (r *Connections) ListDiscovered(ctx context.Context, org, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	ids, err := r.s.q(ctx).ListDiscoveredChannelsForConnection(ctx, sqlc.ListDiscoveredChannelsForConnectionParams{OrganizationID: &org, After: after, Lim: int32(limit)})
	return ids, r.result(ctx, "Channel.Connection.Discovered", err)
}

func (r *Connections) AddCandidate(ctx context.Context, id, ch uuid.UUID) error {
	return r.result(ctx, "Channel.Connection.AddCandidate", r.s.q(ctx).AddChannelConnectionCandidate(ctx, sqlc.AddChannelConnectionCandidateParams{ConnectionID: id, ChannelID: ch}))
}

func (r *Connections) ClearCandidates(ctx context.Context, id uuid.UUID) error {
	return r.result(ctx, "Channel.Connection.ClearCandidates", r.s.q(ctx).ClearChannelConnectionCandidates(ctx, id))
}

func (r *Connections) Candidates(ctx context.Context, id uuid.UUID) ([]domain.ConnectionCandidate, error) {
	rows, err := r.s.q(ctx).ListChannelConnectionCandidates(ctx, id)
	if err = r.result(ctx, "Channel.Connection.Candidates", err); err != nil {
		return nil, err
	}
	out := make([]domain.ConnectionCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.ConnectionCandidate{ID: row.ID, Title: row.Title, Link: row.Link, IconURL: row.IconUrl})
	}
	return out, nil
}

func (r *Connections) HasCandidate(ctx context.Context, id, ch uuid.UUID) (bool, error) {
	ok, err := r.s.q(ctx).HasChannelConnectionCandidate(ctx, sqlc.HasChannelConnectionCandidateParams{ConnectionID: id, ChannelID: ch})
	return ok, r.result(ctx, "Channel.Connection.HasCandidate", err)
}
