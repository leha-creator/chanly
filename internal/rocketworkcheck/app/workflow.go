package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

// Prepare accepts a hash only as content integrity. Authority always comes from
// the independent owner-issued grant stored in the trusted database.
func (s *Service) Prepare(ctx context.Context, m domain.Manifest) (domain.Run, error) {
	if m.Version != 2 || m.Purpose != domain.Diagnostic || m.RunID == "" || m.GrantID == "" || s.Provider == nil {
		return domain.Run{}, domain.ErrDenied
	}
	g, err := s.Store.Grant(ctx, m.GrantID)
	if err != nil {
		return domain.Run{}, domain.ErrDenied
	}
	p := g.Policy
	account, err := s.Store.AccountForAlias(ctx, m.AccountKey)
	if err != nil || account != p.AccountID {
		return domain.Run{}, domain.ErrDenied
	}
	if m.Terms.AmountKopecks != 0 && m.Terms.AmountKopecks != int64(p.FixedAmount) {
		return domain.Run{}, domain.ErrDenied
	}
	m.Terms.AmountKopecks = int64(p.FixedAmount)
	externalID := "rw-diagnostic:" + m.RunID
	if m.LegacyImportID != "" {
		externalID = "integration-check:" + m.LegacyImportID
	}
	if m.Terms.ExternalID != "" && m.Terms.ExternalID != externalID {
		return domain.Run{}, domain.ErrDenied
	}
	m.Terms.ExternalID = externalID
	if m.LegacyImportID != "" && m.ImportedTermsHash != domain.Digest(m.Terms) {
		return domain.Run{}, domain.ErrDenied
	}
	if m.AccountID != p.AccountID || m.CompanyINN != p.CompanyINN || m.Terms.ExecutorINN != m.Executor.INN || m.Terms.EmploymentKind != m.Executor.Employment || !slices.Contains(p.Executors, m.Executor) || m.Terms.Autopayable || m.Terms.Autocomplete || m.Terms.Autosignable {
		return domain.Run{}, domain.ErrDenied
	}
	for _, a := range m.Actions {
		if !slices.Contains(p.Actions, a) {
			return domain.Run{}, domain.ErrDenied
		}
	}
	if len(m.Actions) == 0 || (m.LegacyImportID == "" && !slices.Contains(m.Actions, domain.Create)) || (m.LegacyImportID != "" && slices.Contains(m.Actions, domain.Create)) || domain.Digest(m) != p.ManifestHash || m.RunID != p.RunID {
		return domain.Run{}, domain.ErrDenied
	}
	if err = s.Provider.ValidateManifest(m); err != nil {
		return domain.Run{}, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return domain.Run{}, domain.ErrDenied
	}
	return s.Store.Prepare(ctx, domain.Run{ID: m.RunID, AccountID: p.AccountID, GrantID: g.ID, ExternalID: m.Terms.ExternalID, Nominal: p.FixedAmount, FeeCeiling: p.MaxCommission, ManifestHash: domain.Digest(m), Manifest: b})
}

// Advance performs at most one provider write. All checkpoint changes after a
// network call are conservative: a crash retains the original committed intent.
func (s *Service) Advance(ctx context.Context, id string, execute bool) error {
	r, err := s.Store.Run(ctx, id)
	if err != nil {
		return domain.ErrDenied
	}
	var m domain.Manifest
	if json.Unmarshal(r.Manifest, &m) != nil || domain.Digest(m) != r.ManifestHash {
		return domain.ErrDenied
	}
	o, err := s.Provider.Inspect(ctx, r)
	if err != nil {
		if errors.Is(err, domain.ErrFinancialMismatch) {
			if freezeErr := s.Store.Freeze(ctx, id); freezeErr != nil {
				return freezeErr
			}
		}
		return err
	}
	for _, a := range m.Actions {
		c, cpErr := s.Store.Checkpoint(ctx, id, a)
		if cpErr == nil {
			if c.State == "observed" || c.State == "rejected" {
				continue
			}
			if o.TaskID != nil && observed(a, o.Status) {
				if err = s.Store.Observe(ctx, id, a, "observed", o.TaskID); err != nil {
					return err
				}
				r.TaskID = o.TaskID
				continue
			}
			return ErrReconcile
		}
		if !execute {
			return nil
		}
		allowed := a == domain.Create && o.TaskID == nil
		switch a {
		case domain.Complete:
			allowed = o.CanComplete
		case domain.SignAct:
			allowed = o.CanSign
		case domain.Pay:
			allowed = o.CanPay
		case domain.Cancel:
			allowed = o.CanCancel
		}
		if !allowed || !o.Ready {
			return domain.ErrDenied
		}
		// The adapter invokes this only for the exact expected wire request, at the
		// final HTTP boundary after readiness/ownership checks.
		err = s.Provider.Write(ctx, r, a, func(hash string) error { return s.Intent(ctx, id, a, hash, m.Executor) })
		if err != nil {
			if errors.Is(err, domain.ErrWriteRejected) {
				if observeErr := s.Store.Observe(ctx, id, a, "rejected", r.TaskID); observeErr != nil {
					return observeErr
				}
			}
			s.Log.WarnContext(ctx, "diagnostic write unconfirmed", "run", id, "action", a)
			return err
		}
		return nil // Only an authoritative follow-up read confirms the effect.
	}
	if o.Status == "paid" && o.Fee != nil {
		if o.ProviderPaidAt != nil {
			if err = s.Store.RecordPaidAt(ctx, id, *o.ProviderPaidAt); err != nil {
				if freezeErr := s.Store.Freeze(ctx, id); freezeErr != nil {
					return freezeErr
				}
				return err
			}
		}
		return s.Settle(ctx, id, money.Kopecks(o.Nominal), money.Kopecks(*o.Fee))
	}
	if o.CanceledNoCharge {
		return s.Store.Release(ctx, id)
	}
	return nil
}

func observed(a domain.Action, status string) bool {
	switch a {
	case domain.Create:
		return status != ""
	case domain.Complete:
		return slices.Contains([]string{"accepting", "completed", "paying", "paid"}, status)
	case domain.SignAct:
		return slices.Contains([]string{"completed", "paying", "paid"}, status)
	case domain.Pay:
		return status == "paying" || status == "paid"
	case domain.Cancel:
		return status == "canceled"
	}
	return false
}
