package postgres

import (
	"context"
	"encoding/json"

	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/postgres/sqlc"
)

func (s *Store) AccountForAlias(ctx context.Context, alias string) (string, error) {
	return s.q(ctx).ResolveAlias(ctx, alias)
}
func (s *Store) VerifyRole(ctx context.Context, owner bool) (string, error) {
	r, err := s.q(ctx).ControlPrivileges(ctx)
	if err != nil || r.Superuser || r.PolicyOwner != owner {
		return "", domain.ErrDenied
	}
	return r.Actor, nil
}
func (s *Store) Issue(ctx context.Context, id string, p domain.Policy) error {
	if p.Validate() != nil || p.ArmingID == "" || p.RunID == "" || len(p.ManifestHash) != 64 {
		return domain.ErrDenied
	}
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).IssueGrant(ctx, sqlc.IssueGrantParams{ID: u, Policy: b})
	if err != nil {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic grant issued", "grant", id, "account", p.AccountID)
	return nil
}
func (s *Store) Revoke(ctx context.Context, id string) error {
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).RevokeGrant(ctx, u); err != nil {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic grant revoked", "grant", id)
	return nil
}

func (s *Store) Reauthorize(ctx context.Context, run, grant string) error {
	r, err := uid(run)
	if err != nil {
		return domain.ErrDenied
	}
	g, err := uid(grant)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).ReauthorizeRun(ctx, sqlc.ReauthorizeRunParams{RunID: r, GrantID: g}); err != nil {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic run reauthorized without resetting intent", "run", run, "grant", grant)
	return nil
}
func (s *Store) Kill(ctx context.Context, account string, killed bool) error {
	if err := s.q(ctx).KillAccount(ctx, sqlc.KillAccountParams{AccountID: account, Killed: killed}); err != nil {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic account switch changed", "account", account, "killed", killed)
	return nil
}
func (s *Store) Configure(ctx context.Context, p domain.Policy, alias string) error {
	if p.Validate() != nil || alias == "" {
		return domain.ErrDenied
	}
	if err := s.q(ctx).ConfigureAccount(ctx, sqlc.ConfigureAccountParams{ID: p.AccountID, CompanyInn: p.CompanyINN, TotalLimit: int64(p.TotalBudget), DailyLimit: int64(p.DailyBudget), DailyOperations: p.DailyOperations}); err != nil {
		return domain.ErrDenied
	}
	if err := s.q(ctx).BindAccountAlias(ctx, sqlc.BindAccountAliasParams{Alias: alias, AccountID: p.AccountID}); err != nil {
		return domain.ErrDenied
	}
	account, err := s.AccountForAlias(ctx, alias)
	if err != nil || account != p.AccountID {
		return domain.ErrDenied
	}
	a, err := s.q(ctx).GetAccount(ctx, p.AccountID)
	if err != nil || a.CompanyInn != p.CompanyINN {
		return domain.ErrDenied
	}
	s.log.InfoContext(ctx, "diagnostic account policy configured", "account", p.AccountID, "total_limit", p.TotalBudget, "daily_limit", p.DailyBudget, "daily_operations", p.DailyOperations)
	return nil
}

func (s *Store) ImportLegacy(ctx context.Context, id, account, company, external string, task int64, hash, terms, status string) error {
	a, err := s.q(ctx).GetAccount(ctx, account)
	if err != nil || a.CompanyInn != company {
		return domain.ErrDenied
	}
	u, err := uid(id)
	if err != nil {
		return domain.ErrDenied
	}
	if err = s.q(ctx).ImportLegacy(ctx, sqlc.ImportLegacyParams{ID: u, AccountID: account, ExternalID: external, TaskID: task, ManifestHash: hash, TermsHash: terms, Status: status}); err != nil {
		return domain.ErrDenied
	}
	return nil
}
