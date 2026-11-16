package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// PayoutDetails — данные организации-блогера для выплаты (модуль payout, 06 §8): тип лица,
// ИНН, ФИО исполнителя и телефон для регистрации в Рокет Ворк.
type PayoutDetails struct {
	ID        uuid.UUID
	LegalType string
	INN       string
	// FullName — ФИО из профиля блогера; пусто — контактное лицо, затем название.
	FullName  string
	Phone     string
	TaxRegime string
}

// ForPayout читает данные без блокировки; нет организации — domain.ErrNotFound.
func (r *Organizations) ForPayout(ctx context.Context, id uuid.UUID) (PayoutDetails, error) {
	const op = "Account.Organizations.ForPayout"
	row, err := r.s.q(ctx).GetOrganizationForPayout(ctx, id)
	if err != nil {
		return PayoutDetails{}, r.s.fail(ctx, op, err)
	}
	name := strings.TrimSpace(row.FullName)
	if name == "" {
		name = strings.TrimSpace(row.ContactName)
	}
	if name == "" {
		name = row.Name
	}
	var tax string
	if row.TaxRegime != nil {
		tax = *row.TaxRegime
	}
	return PayoutDetails{ID: row.ID, LegalType: row.LegalType, INN: row.Inn, FullName: name, Phone: row.Phone, TaxRegime: tax}, nil
}

func (r *Organizations) BloggerOfferAcceptedAt(ctx context.Context, id uuid.UUID, before time.Time) (time.Time, error) {
	at, err := r.s.q(ctx).GetBloggerOfferAcceptanceForAct(ctx, sqlc.GetBloggerOfferAcceptanceForActParams{OrganizationID: id, BeforeAt: before})
	if err != nil {
		return time.Time{}, r.s.fail(ctx, "Account.BloggerOfferAcceptedAt", err)
	}
	return at, nil
}
