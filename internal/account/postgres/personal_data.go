package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// PersonalData — выгрузка и обезличивание данных субъекта (R-26). Почта в журнал не пишется.
type PersonalData struct{ s *Store }

// PersonalData возвращает репозиторий данных субъекта.
func (s *Store) PersonalData() *PersonalData { return &PersonalData{s} }

func (r *PersonalData) Acceptances(ctx context.Context, userID uuid.UUID) ([]domain.SubjectAcceptance, error) {
	const op = "Account.PersonalData.Acceptances"
	r.s.debug(ctx, op, "user_id", userID)
	rows, err := r.s.q(ctx).ListSubjectAcceptances(ctx, userID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.SubjectAcceptance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.SubjectAcceptance{Kind: domain.LegalDocumentKind(row.Kind), Version: int(row.Version),
			AcceptedAt: row.AcceptedAt, IP: row.Ip})
	}
	return out, nil
}

func (r *PersonalData) SubjectSessions(ctx context.Context, userID uuid.UUID) ([]domain.SubjectSession, error) {
	const op = "Account.PersonalData.SubjectSessions"
	r.s.debug(ctx, op, "user_id", userID)
	rows, err := r.s.q(ctx).ListSubjectSessions(ctx, userID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.SubjectSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.SubjectSession{CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt,
			IP: row.Ip, UserAgent: row.UserAgent})
	}
	return out, nil
}

func (r *PersonalData) MemberCount(ctx context.Context, organizationID uuid.UUID) (int, error) {
	const op = "Account.PersonalData.MemberCount"
	n, err := r.s.q(ctx).CountOrganizationMembers(ctx, organizationID)
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	return int(n), nil
}

func (r *PersonalData) SaveAnonymized(ctx context.Context, u *domain.User) error {
	const op = "Account.PersonalData.SaveAnonymized"
	r.s.debug(ctx, op, "user_id", u.ID)
	err := r.s.q(ctx).SetUserAnonymized(ctx, sqlc.SetUserAnonymizedParams{
		ID: u.ID, Email: u.Email, Status: string(u.Status), BlockedAt: u.BlockedAt, BlockedReason: u.BlockedReason,
		AnonymizedAt: u.AnonymizedAt, At: u.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *PersonalData) ClearSessions(ctx context.Context, userID uuid.UUID, at time.Time) (int64, error) {
	const op = "Account.PersonalData.ClearSessions"
	n, err := r.s.q(ctx).ClearUserSessions(ctx, sqlc.ClearUserSessionsParams{UserID: userID, At: at})
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	return n, nil
}

func (r *PersonalData) DeleteLoginCodes(ctx context.Context, email string) (int64, error) {
	const op = "Account.PersonalData.DeleteLoginCodes"
	n, err := r.s.q(ctx).DeleteLoginCodesByEmail(ctx, &email)
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	return n, nil
}

func (r *PersonalData) ClearOrganizationContacts(ctx context.Context, organizationID uuid.UUID, at time.Time) error {
	const op = "Account.PersonalData.ClearOrganizationContacts"
	r.s.debug(ctx, op, "organization_id", organizationID)
	if err := r.s.q(ctx).ClearOrganizationContacts(ctx, sqlc.ClearOrganizationContactsParams{ID: organizationID, At: at}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}
