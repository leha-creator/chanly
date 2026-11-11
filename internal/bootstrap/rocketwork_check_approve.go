package bootstrap

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/platform/money"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	checkpg "github.com/leha-creator/chanly/internal/rocketworkcheck/postgres"
)

// Operator input omits generated identities and hashes. Zero commission must be
// explicit; no financial defaults or nominal downscaling are introduced.
type rwUILimits struct {
	Version                  int64
	MaxCommission            *money.Kopecks
	TotalBudget, DailyBudget money.Kopecks
	DailyOperations          int64
	NotBefore, ExpiresAt     time.Time
}

func approveRWUI(ctx context.Context, store *checkpg.Store, manifest domain.Manifest, limitsPath, arming string) (domain.Run, error) {
	var limits rwUILimits
	if readRWJSON(limitsPath, &limits) != nil || limits.MaxCommission == nil || arming == "" ||
		!slices.Contains(manifest.Actions, domain.Create) || !slices.Contains(manifest.Actions, domain.Pay) || slices.Contains(manifest.Actions, domain.Cancel) ||
		manifest.LegacyImportID != "" || manifest.ImportedTermsHash != "" || manifest.Target != "production" || !rwOrigin(manifest.Target, manifest.Origin) {
		return domain.Run{}, domain.ErrDenied
	}
	manifest.Version = 2
	manifest.Purpose = domain.Diagnostic
	manifest.RunID = uuid.NewString()
	manifest.GrantID = uuid.NewString()
	manifest.Terms.ExternalID = "rw-diagnostic:" + manifest.RunID
	if checkTermsForManifest(manifest) != nil {
		return domain.Run{}, domain.ErrDenied
	}
	p := domain.Policy{ArmingID: arming, RunID: manifest.RunID, ManifestHash: domain.Digest(manifest), AccountID: manifest.AccountID,
		CompanyINN: manifest.CompanyINN, Version: limits.Version, FixedAmount: money.Kopecks(manifest.Terms.AmountKopecks),
		MaxCommission: *limits.MaxCommission, TotalBudget: limits.TotalBudget, DailyBudget: limits.DailyBudget,
		DailyOperations: limits.DailyOperations, Executors: []domain.Executor{manifest.Executor}, Actions: manifest.Actions,
		NotBefore: limits.NotBefore, ExpiresAt: limits.ExpiresAt}
	if p.Validate() != nil {
		return domain.Run{}, domain.ErrDenied
	}
	provider := bridge.CheckProvider{Origin: manifest.Origin, Target: manifest.Target}
	if provider.ValidateManifest(manifest) != nil {
		return domain.Run{}, domain.ErrDenied
	}
	if err := store.Configure(ctx, p, manifest.AccountKey); err != nil {
		return domain.Run{}, err
	}
	if err := store.Issue(ctx, manifest.GrantID, p); err != nil {
		return domain.Run{}, err
	}
	s := checkapp.Service{Store: store, Provider: provider}
	return s.Prepare(ctx, manifest)
}
