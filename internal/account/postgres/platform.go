package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// platformPhone — телефон строки платформы: поле обязательно, но для платформы не используется.
const platformPhone = "+70000000000"

var _ domain.PlatformOrganizations = (*Organizations)(nil)

// Platform serializes singleton bootstrap inside its READ COMMITTED transaction.
// Lock and SELECT must be separate statements: a SELECT snapshot taken before
// waiting for the lock could still miss the other startup's committed row.
func (r *Organizations) Platform(ctx context.Context) (*domain.PlatformOrganization, error) {
	const op = "Account.Organizations.Platform"
	if _, ok := tx.FromContext(ctx); !ok {
		return nil, fmt.Errorf("%s requires a transaction", op)
	}
	r.s.debug(ctx, op, "phase", "[FIX] waiting for platform bootstrap lock")
	if err := r.s.q(ctx).LockPlatformOrganization(ctx); err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "phase", "[FIX] platform bootstrap lock acquired")
	row, err := r.s.q(ctx).GetPlatformOrganization(ctx)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return &domain.PlatformOrganization{ID: row.ID, LegalType: domain.LegalType(row.LegalType), INN: row.Inn, Name: row.Name}, nil
}

// InsertPlatform вставляет строку платформы; ИНН занят — apperr.INNTaken.
func (r *Organizations) InsertPlatform(ctx context.Context, p domain.PlatformOrganization, now time.Time) error {
	const op = "Account.Organizations.InsertPlatform"
	r.s.debug(ctx, op, "organization_id", p.ID)
	err := r.s.q(ctx).InsertPlatformOrganization(ctx, sqlc.InsertPlatformOrganizationParams{
		ID: p.ID, LegalType: string(p.LegalType), Inn: p.INN, Name: p.Name, Phone: platformPhone, Now: now,
	})
	if db.UniqueViolation(err, "organization_inn_key") {
		return apperr.INNTaken()
	}
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

// RenamePlatform меняет наименование платформы.
func (r *Organizations) RenamePlatform(ctx context.Context, id uuid.UUID, name string, now time.Time) error {
	const op = "Account.Organizations.RenamePlatform"
	r.s.debug(ctx, op, "organization_id", id)
	if err := r.s.q(ctx).RenamePlatformOrganization(ctx, sqlc.RenamePlatformOrganizationParams{ID: id, Name: name, Now: now}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

// MarkingDetails — реквизиты организации для ОРД (модуль marking); без блокировки.
type MarkingDetails struct {
	VATRate    string
	ID         uuid.UUID
	LegalType  string
	INN        string
	Name       string
	KPP        string
	IsPlatform bool
}

// ForMarking читает реквизиты для ОРД; нет организации — domain.ErrNotFound.
func (r *Organizations) ForMarking(ctx context.Context, id uuid.UUID) (MarkingDetails, error) {
	const op = "Account.Organizations.ForMarking"
	row, err := r.s.q(ctx).GetOrganizationForMarking(ctx, id)
	if err != nil {
		return MarkingDetails{}, r.s.fail(ctx, op, err)
	}
	d := MarkingDetails{ID: row.ID, LegalType: row.LegalType, INN: row.Inn, Name: row.Name, IsPlatform: row.IsPlatform}
	if row.VatRate != nil {
		d.VATRate = *row.VatRate
	}
	if row.Kpp != nil {
		d.KPP = *row.Kpp
	}
	return d, nil
}

// FirstOfferAcceptance — первое принятие оферты вида kind; false — не принимала.
func (r *Organizations) FirstOfferAcceptance(ctx context.Context, orgID uuid.UUID, kind domain.LegalDocumentKind) (time.Time, int, bool, error) {
	const op = "Account.Organizations.FirstOfferAcceptance"
	row, err := r.s.q(ctx).FirstOfferAcceptance(ctx, sqlc.FirstOfferAcceptanceParams{OrganizationID: orgID, Kind: string(kind)})
	if err != nil {
		if err = r.s.fail(ctx, op, err); errors.Is(err, domain.ErrNotFound) {
			return time.Time{}, 0, false, nil
		}
		return time.Time{}, 0, false, err
	}
	return row.AcceptedAt.UTC(), int(row.Version), true, nil
}
