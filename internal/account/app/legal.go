package app

import (
	"context"
	"net/netip"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// LegalDocumentView — действующий документ для кабинета.
type LegalDocumentView struct {
	Document domain.LegalDocument
	URL      string
	Accepted bool
}

// ListLegalDocuments возвращает действующие версии с отметкой о принятии
// организацией и подписанной ссылкой на файл.
func (s *Service) ListLegalDocuments(ctx context.Context, p Principal) ([]LegalDocumentView, error) {
	current, err := s.Legal.Current(ctx)
	if err != nil {
		return nil, err
	}
	accepted := map[uuid.UUID]bool{}
	if p.OrganizationID != nil {
		if accepted, err = s.Legal.AcceptedIDs(ctx, *p.OrganizationID); err != nil {
			return nil, err
		}
	}
	out := make([]LegalDocumentView, 0, len(current))
	for _, k := range domain.LegalDocumentKinds { // постоянный порядок видов
		for _, d := range current {
			if d.Kind != k {
				continue
			}
			url, err := s.Storage.PresignGet(ctx, d.StorageKey, s.Config.DocumentURLTTL)
			if err != nil {
				return nil, err
			}
			out = append(out, LegalDocumentView{Document: d, URL: url, Accepted: accepted[d.ID]})
		}
	}
	return out, nil
}

// AcceptLegalDocuments записывает принятие документов организацией (S-01 шаг 5,
// S-10 шаг 3). Принять можно только действующую версию документа, нужного
// открытым кабинетам. Повторное принятие ничего не меняет.
func (s *Service) AcceptLegalDocuments(ctx context.Context, p Principal, docIDs []uuid.UUID, ip *netip.Addr) error {
	const op = "Account.AcceptLegalDocuments"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.organization(ctx, p)
		if err != nil {
			return apperr.Forbidden("organization_required")
		}
		current, err := s.Legal.Current(ctx)
		if err != nil {
			return err
		}
		required := domain.RequiredDocuments(o.Cabinets())
		byID := map[uuid.UUID]domain.LegalDocument{}
		for _, d := range current {
			byID[d.ID] = d
		}
		for _, id := range docIDs {
			d, ok := byID[id]
			if !ok {
				return apperr.NotFound("legal document is not found or outdated").WithDetail("legal_document_id", id)
			}
			if !slices.Contains(required, d.Kind) {
				return apperr.Validation("legal_document_ids", "not_required").WithDetail("legal_document_id", id)
			}
		}
		now := s.Clock.Now()
		for _, id := range docIDs {
			if err := s.Legal.Accept(ctx, domain.LegalAcceptance{
				ID: ids.New(), LegalDocumentID: id, UserID: p.UserID, OrganizationID: o.ID, AcceptedAt: now, IP: ip,
			}); err != nil {
				return err
			}
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: "legal.accept", ObjectType: "organization", ObjectID: o.ID.String(), ActorType: p.actorType(),
			Data: map[string]any{"legal_document_ids": docIDs},
		})
	})
	if err != nil {
		if e, ok := apperr.As(err); ok && e.Code != apperr.CodeInternal {
			s.log.WarnContext(ctx, "legal documents rejected", logx.KeyOp, op, "code", e.Code, "reason", e.Details["reason"])
		}
		return err
	}
	s.log.InfoContext(ctx, "legal documents accepted", logx.KeyOp, op, "ids", docIDs)
	return nil
}
