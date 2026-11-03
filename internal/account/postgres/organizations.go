package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// Organizations — организации, участники и профили.
type Organizations struct{ s *Store }

func (r *Organizations) Create(ctx context.Context, o *domain.Organization, owner uuid.UUID) error {
	const op = "Account.Organizations.Create"
	r.s.debug(ctx, op, "organization_id", o.ID, "user_id", owner)
	q := r.s.q(ctx)
	err := q.InsertOrganization(ctx, sqlc.InsertOrganizationParams{
		ID: o.ID, LegalType: string(o.LegalType), Inn: o.INN, Name: o.Name, ContactName: o.ContactName,
		Phone: o.Phone, Kpp: o.KPP, Address: o.Address, TaxRegime: strPtr(o.TaxRegime), VatRate: strPtr(o.VatRate),
		TaxDocumentStorageKey: taxKey(o), TaxDocumentSha256: taxSum(o),
		SignerPosition: o.SignerPosition, SignerName: o.SignerName, SignerBasisName: o.SignerBasisName,
		SignerBasisNumber: o.SignerBasisNumber, BankAccount: o.BankAccount, BankName: o.BankName,
		BankCorrAccount: o.BankCorrAccount, BankBik: o.BankBIK, Status: string(o.Status),
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	})
	if db.UniqueViolation(err, "organization_inn_key") {
		r.s.log.DebugContext(ctx, "inn is already registered", "op", op)
		return apperr.INNTaken()
	}
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	err = q.InsertOrganizationMember(ctx, sqlc.InsertOrganizationMemberParams{
		ID: ids.New(), OrganizationID: o.ID, UserID: owner, Role: string(domain.MemberOwner), Now: o.CreatedAt,
	})
	if db.UniqueViolation(err, "organization_member_user_id_key") {
		return apperr.Conflict("user already has an organization") // A-13
	}
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return r.saveProfiles(ctx, q, o, op)
}

func (r *Organizations) Update(ctx context.Context, o *domain.Organization) error {
	const op = "Account.Organizations.Update"
	r.s.debug(ctx, op, "organization_id", o.ID)
	q := r.s.q(ctx)
	err := q.UpdateOrganization(ctx, sqlc.UpdateOrganizationParams{
		ID: o.ID, Name: o.Name, ContactName: o.ContactName, Phone: o.Phone, Kpp: o.KPP, Address: o.Address,
		TaxRegime: strPtr(o.TaxRegime), VatRate: strPtr(o.VatRate),
		TaxDocumentStorageKey: taxKey(o), TaxDocumentSha256: taxSum(o),
		SignerPosition: o.SignerPosition, SignerName: o.SignerName, SignerBasisName: o.SignerBasisName,
		SignerBasisNumber: o.SignerBasisNumber, BankAccount: o.BankAccount, BankName: o.BankName,
		BankCorrAccount: o.BankCorrAccount, BankBik: o.BankBIK, Status: string(o.Status), UpdatedAt: o.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return r.saveProfiles(ctx, q, o, op)
}

// saveProfiles создаёт профили открытых кабинетов; профиль блогера обновляет.
func (r *Organizations) saveProfiles(ctx context.Context, q *sqlc.Queries, o *domain.Organization, op string) error {
	if o.Advertiser {
		if err := q.InsertAdvertiserProfile(ctx, sqlc.InsertAdvertiserProfileParams{OrganizationID: o.ID, Now: o.UpdatedAt}); err != nil {
			return r.s.fail(ctx, op, err)
		}
	}
	if o.Blogger != nil {
		err := q.UpsertBloggerProfile(ctx, sqlc.UpsertBloggerProfileParams{
			OrganizationID: o.ID, FullName: o.Blogger.FullName, Now: o.UpdatedAt,
		})
		if err != nil {
			return r.s.fail(ctx, op, err)
		}
	}
	return nil
}

func (r *Organizations) GetByUser(ctx context.Context, userID uuid.UUID) (*domain.Organization, error) {
	const op = "Account.Organizations.GetByUser"
	r.s.debug(ctx, op, "user_id", userID)
	row, err := r.s.q(ctx).GetOrganizationByUser(ctx, userID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return organizationFromRow(row.Organization, row.IsAdvertiser, row.IsBlogger, row.BloggerFullName), nil
}

func (r *Organizations) GetForUpdate(ctx context.Context, organizationID uuid.UUID) (*domain.Organization, error) {
	const op = "Account.Organizations.GetForUpdate"
	r.s.debug(ctx, op, "organization_id", organizationID)
	row, err := r.s.q(ctx).GetOrganizationForUpdate(ctx, organizationID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return organizationFromRow(row.Organization, row.IsAdvertiser, row.IsBlogger, row.BloggerFullName), nil
}

func (r *Organizations) INNTaken(ctx context.Context, inn string) (bool, error) {
	const op = "Account.Organizations.INNTaken"
	taken, err := r.s.q(ctx).INNExists(ctx, inn)
	if err != nil {
		return false, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "taken", taken)
	return taken, nil
}

func organizationFromRow(o sqlc.Organization, advertiser, blogger bool, fullName *string) *domain.Organization {
	out := &domain.Organization{
		ID: o.ID, LegalType: domain.LegalType(o.LegalType), INN: o.Inn, Name: o.Name, ContactName: o.ContactName,
		Phone: o.Phone, Address: o.Address, KPP: o.Kpp,
		TaxRegime: enumPtr[domain.TaxRegime](o.TaxRegime), VatRate: enumPtr[domain.VatRate](o.VatRate),
		SignerPosition: o.SignerPosition, SignerName: o.SignerName, SignerBasisName: o.SignerBasisName,
		SignerBasisNumber: o.SignerBasisNumber, BankAccount: o.BankAccount, BankName: o.BankName,
		BankCorrAccount: o.BankCorrAccount, BankBIK: o.BankBik, Status: domain.Status(o.Status),
		Advertiser: advertiser, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if o.TaxDocumentStorageKey != nil && o.TaxDocumentSha256 != nil {
		out.TaxDocument = &domain.FileRef{StorageKey: *o.TaxDocumentStorageKey, SHA256: *o.TaxDocumentSha256}
	}
	if blogger {
		out.Blogger = &domain.BloggerProfile{FullName: fullName}
	}
	return out
}

func taxKey(o *domain.Organization) *string {
	if o.TaxDocument == nil {
		return nil
	}
	return &o.TaxDocument.StorageKey
}

func taxSum(o *domain.Organization) *string {
	if o.TaxDocument == nil {
		return nil
	}
	return &o.TaxDocument.SHA256
}

// LegalDocuments — оферты и их принятие.
type LegalDocuments struct{ s *Store }

func (r *LegalDocuments) Current(ctx context.Context) ([]domain.LegalDocument, error) {
	const op = "Account.LegalDocuments.Current"
	rows, err := r.s.q(ctx).CurrentLegalDocuments(ctx)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", len(rows))
	out := make([]domain.LegalDocument, 0, len(rows))
	for _, d := range rows {
		out = append(out, domain.LegalDocument{
			ID: d.ID, Kind: domain.LegalDocumentKind(d.Kind), Version: int(d.Version), PublishedAt: d.PublishedAt,
			StorageKey: d.StorageKey, SHA256: d.Sha256, CreatedAt: d.CreatedAt,
		})
	}
	return out, nil
}

func (r *LegalDocuments) AcceptedIDs(ctx context.Context, organizationID uuid.UUID) (map[uuid.UUID]bool, error) {
	const op = "Account.LegalDocuments.AcceptedIDs"
	rows, err := r.s.q(ctx).AcceptedLegalDocumentIDs(ctx, organizationID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "organization_id", organizationID, "count", len(rows))
	out := make(map[uuid.UUID]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

func (r *LegalDocuments) Accept(ctx context.Context, a domain.LegalAcceptance) error {
	const op = "Account.LegalDocuments.Accept"
	r.s.debug(ctx, op, "organization_id", a.OrganizationID, "legal_document_id", a.LegalDocumentID)
	err := r.s.q(ctx).InsertLegalAcceptance(ctx, sqlc.InsertLegalAcceptanceParams{
		ID: a.ID, LegalDocumentID: a.LegalDocumentID, UserID: a.UserID, OrganizationID: a.OrganizationID,
		AcceptedAt: a.AcceptedAt, Ip: a.IP,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *LegalDocuments) LatestVersion(ctx context.Context, kind domain.LegalDocumentKind) (int, error) {
	const op = "Account.LegalDocuments.LatestVersion"
	v, err := r.s.q(ctx).LatestLegalVersion(ctx, string(kind))
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "kind", kind, "version", v)
	return int(v), nil
}

func (r *LegalDocuments) Create(ctx context.Context, d *domain.LegalDocument) error {
	const op = "Account.LegalDocuments.Create"
	r.s.debug(ctx, op, "legal_document_id", d.ID, "kind", d.Kind, "version", d.Version)
	err := r.s.q(ctx).InsertLegalDocument(ctx, sqlc.InsertLegalDocumentParams{
		ID: d.ID, Kind: string(d.Kind), Version: int32(d.Version), PublishedAt: d.PublishedAt,
		StorageKey: d.StorageKey, Sha256: d.SHA256, CreatedAt: d.CreatedAt,
	})
	if db.UniqueViolation(err, "legal_document_kind_version_key") {
		return apperr.Conflict("legal document version already exists")
	}
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

// Requisites — название и ИНН организации для маркировки поста (модуль post, R-14).
// Читает без блокировки строки; нет организации — domain.ErrNotFound.
func (r *Organizations) Requisites(ctx context.Context, organizationID uuid.UUID) (name, inn string, err error) {
	const op = "Account.Organizations.Requisites"
	row, err := r.s.q(ctx).GetOrganizationRequisites(ctx, organizationID)
	if err != nil {
		return "", "", r.s.fail(ctx, op, err)
	}
	return row.Name, row.Inn, nil
}
