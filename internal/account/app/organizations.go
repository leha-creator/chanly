package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// MaxTaxDocumentSize — предел документа о режиме налогообложения (R-5).
const MaxTaxDocumentSize = 10 << 20

// taxDocumentTypes — допустимые типы документа и расширения ключа в хранилище (R-5).
var taxDocumentTypes = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
}

// Me — текущий пользователь, его организации и документы к принятию.
type Me struct {
	User             *domain.User
	Organizations    []*domain.Organization
	PendingDocuments []domain.LegalDocument
}

// GetMe возвращает данные для GET /me.
func (s *Service) GetMe(ctx context.Context, p Principal) (*Me, error) {
	u, err := s.Users.GetByID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	me := &Me{User: u}
	o, err := s.Organizations.GetByUser(ctx, p.UserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return me, nil
	case err != nil:
		return nil, err
	}
	me.Organizations = []*domain.Organization{o}
	pending, err := s.pendingDocuments(ctx, o)
	if err != nil {
		return nil, err
	}
	me.PendingDocuments = pending
	return me, nil
}

func (s *Service) pendingDocuments(ctx context.Context, o *domain.Organization) ([]domain.LegalDocument, error) {
	current, err := s.Legal.Current(ctx)
	if err != nil {
		return nil, err
	}
	accepted, err := s.Legal.AcceptedIDs(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	return domain.PendingDocuments(current, o.Cabinets(), accepted), nil
}

// CreateOrganization создаёт организацию с кабинетом или открывает второй
// кабинет у существующей (S-01 шаг 3, S-10 шаг 2). Возвращает организацию.
func (s *Service) CreateOrganization(ctx context.Context, p Principal, in domain.NewOrganizationInput) (*domain.Organization, error) {
	const op = "Account.CreateOrganization"
	var (
		out   *domain.Organization
		added bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		existing, err := s.Organizations.GetByUser(ctx, p.UserID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if existing == nil {
			o, err := domain.NewOrganization(ids.New(), in, now)
			if err != nil {
				return err
			}
			taken, err := s.Organizations.INNTaken(ctx, o.INN)
			if err != nil {
				return err
			}
			if taken {
				return apperr.INNTaken()
			}
			if err := s.Organizations.Create(ctx, o, p.UserID); err != nil {
				return err
			}
			out = o
			return s.Audit.Record(ctx, audit.Entry{
				Action: "organization.create", ObjectType: "organization", ObjectID: o.ID.String(),
				ActorType: actorForCabinet(in.Cabinet), OrganizationID: &o.ID,
				Data: map[string]any{"after": audit.Mask(o.Values())},
			})
		}

		o, err := s.Organizations.GetForUpdate(ctx, existing.ID)
		if err != nil {
			return err
		}
		if in.INN != o.INN || in.LegalType != o.LegalType {
			return apperr.Validation("inn", domain.ReasonImmutable)
		}
		before := o.Values()
		if err := o.AddCabinet(in.Cabinet, in.Fields, now); err != nil {
			return err
		}
		if err := s.Organizations.Update(ctx, o); err != nil {
			return err
		}
		out, added = o, true
		after := o.Values()
		return s.Audit.Record(ctx, audit.Entry{
			Action: "organization.add_cabinet", ObjectType: "organization", ObjectID: o.ID.String(),
			ActorType: actorForCabinet(in.Cabinet), OrganizationID: &o.ID,
			Data: audit.Diff(before, after, domain.ChangedFields(before, after)),
		})
	})
	if err != nil {
		s.logRejected(ctx, op, err)
		return nil, err
	}
	octx := logx.With(ctx, logx.KeyOrganizationID, out.ID.String())
	if added {
		s.log.InfoContext(octx, "cabinet added", logx.KeyOp, op, "cabinet", in.Cabinet)
	} else {
		s.log.InfoContext(octx, "organization created", logx.KeyOp, op, "cabinet", in.Cabinet, "legal_type", out.LegalType)
	}
	return out, nil
}

// GetOrganization возвращает организацию пользователя.
func (s *Service) GetOrganization(ctx context.Context, p Principal) (*domain.Organization, error) {
	return s.organization(ctx, p)
}

// UpdateOrganization меняет реквизиты (PATCH). ИНН и тип лица не меняются.
func (s *Service) UpdateOrganization(ctx context.Context, p Principal, f domain.Fields) (*domain.Organization, error) {
	const op = "Account.UpdateOrganization"
	var (
		out     *domain.Organization
		changed []string
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.organization(ctx, p)
		if err != nil {
			return err
		}
		o, err := s.Organizations.GetForUpdate(ctx, current.ID)
		if err != nil {
			return err
		}
		before := o.Values()
		if changed, err = o.Update(f, s.Clock.Now()); err != nil {
			return err
		}
		out = o
		if len(changed) == 0 {
			return nil
		}
		if err := s.Organizations.Update(ctx, o); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "organization.update", ObjectType: "organization", ObjectID: o.ID.String(),
			ActorType: p.actorType(), Data: audit.Diff(before, o.Values(), changed),
		}); err != nil {
			return err
		}
		// R-15 этапа «Маркировка»: наименование и КПП уходят в ОРД — контрагент отправляется заново.
		if slices.Contains(changed, "name") || slices.Contains(changed, "kpp") {
			s.log.DebugContext(ctx, "organization change passed to marking", logx.KeyOp, op, logx.KeyOrganizationID, o.ID)
			return s.organizationListener().OrganizationChanged(ctx, o.ID)
		}
		return nil
	})
	if err != nil {
		s.logRejected(ctx, op, err)
		return nil, err
	}
	s.log.InfoContext(ctx, "organization updated", logx.KeyOp, op, "fields", changed)
	return out, nil
}

// UploadTaxDocument сохраняет документ о режиме налогообложения блогера (R-5).
// Тип определяется по содержимому; прежний файл остаётся в хранилище, история — в журнале действий.
func (s *Service) UploadTaxDocument(ctx context.Context, p Principal, data []byte) (*domain.Organization, error) {
	const op = "Account.UploadTaxDocument"
	current, err := s.organization(ctx, p)
	if err != nil {
		return nil, err
	}
	if current.Blogger == nil {
		return nil, apperr.Forbidden(domain.ReasonCabinetRequired)
	}
	now := s.Clock.Now()
	if s.Uploads != nil {
		n, err := s.Uploads.CountByUserSince(ctx, actionTaxDocumentUpload, p.UserID, now.Add(-time.Hour))
		if err != nil {
			return nil, err
		}
		if n >= s.Config.TaxDocumentUploadsPerHour {
			s.log.WarnContext(ctx, "tax document upload rate limited", logx.KeyOp, op, "scope", "user")
			return nil, apperr.RateLimited("user", time.Hour)
		}
	}
	if len(data) == 0 {
		return nil, apperr.Validation("file", "empty")
	}
	if len(data) > MaxTaxDocumentSize {
		return nil, apperr.Validation("file", "too_large")
	}
	contentType := http.DetectContentType(data) // по содержимому, не по расширению (07 §4)
	ext, ok := taxDocumentTypes[contentType]
	if !ok {
		s.log.WarnContext(ctx, "tax document rejected", logx.KeyOp, op, "reason", "unsupported_type", "content_type", contentType)
		return nil, apperr.Validation("file", "unsupported_type")
	}
	sum := sha256.Sum256(data)
	ref := domain.FileRef{StorageKey: "tax_document/" + ids.New().String() + ext, SHA256: hex.EncodeToString(sum[:])}
	// Файл кладётся до транзакции: внешний вызов не держит транзакцию открытой.
	if err := s.Storage.PutPrivate(ctx, ref.StorageKey, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		return nil, err
	}

	var out *domain.Organization
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Organizations.GetForUpdate(ctx, current.ID)
		if err != nil {
			return err
		}
		if err := o.SetTaxDocument(ref, s.Clock.Now()); err != nil {
			return err
		}
		if err := s.Organizations.Update(ctx, o); err != nil {
			return err
		}
		out = o
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionTaxDocumentUpload, ObjectType: "organization", ObjectID: o.ID.String(),
			ActorType: audit.ActorBlogger,
			Data:      map[string]any{"sha256": ref.SHA256, "size": len(data), "content_type": contentType},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "tax document uploaded", logx.KeyOp, op, "size", len(data), "content_type", contentType)
	return out, nil
}

const actionTaxDocumentUpload = "organization.tax_document_upload"

func actorForCabinet(c domain.Cabinet) string {
	if c == domain.CabinetBlogger {
		return audit.ActorBlogger
	}
	return audit.ActorAdvertiser
}

// logRejected пишет предметный отказ: поле и причину, без значений.
func (s *Service) logRejected(ctx context.Context, op string, err error) {
	e, ok := apperr.As(err)
	if !ok || e.Code == apperr.CodeInternal {
		return
	}
	s.log.WarnContext(ctx, "organization rejected", logx.KeyOp, op, "code", e.Code,
		"field", e.Details["field"], "reason", e.Details["reason"])
}
