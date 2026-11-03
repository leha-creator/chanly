package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// PublishLegalDocument публикует следующую версию оферты или правил (R-8):
// файл PDF кладётся в закрытую область хранилища, версия — наибольшая + 1.
// До появления API администратора вызывается командой `maxim legal publish`.
func (s *Service) PublishLegalDocument(ctx context.Context, kind domain.LegalDocumentKind, data []byte, publishedAt time.Time) (*domain.LegalDocument, error) {
	const op = "Account.PublishLegalDocument"
	if !kind.Valid() {
		return nil, apperr.Validation("kind", domain.ReasonInvalidValue)
	}
	if http.DetectContentType(data) != "application/pdf" {
		return nil, apperr.Validation("file", "unsupported_type")
	}
	sum := sha256.Sum256(data)
	key := "legal/" + ids.New().String() + ".pdf"
	if err := s.Storage.PutPrivate(ctx, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		s.log.ErrorContext(ctx, "legal document upload failed", logx.KeyOp, op, "kind", kind, logx.KeyError, err.Error())
		return nil, err
	}
	var doc *domain.LegalDocument
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		latest, err := s.Legal.LatestVersion(ctx, kind)
		if err != nil {
			return err
		}
		doc = &domain.LegalDocument{
			ID: ids.New(), Kind: kind, Version: latest + 1, PublishedAt: publishedAt,
			StorageKey: key, SHA256: hex.EncodeToString(sum[:]), CreatedAt: s.Clock.Now(),
		}
		if err := s.Legal.Create(ctx, doc); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: "legal.publish", ObjectType: "legal_document", ObjectID: doc.ID.String(), ActorType: audit.ActorSystem,
			Data: map[string]any{"kind": kind, "version": doc.Version, "sha256": doc.SHA256, "published_at": publishedAt},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "legal document published", logx.KeyOp, op, "kind", kind, "version", doc.Version, "sha256", doc.SHA256)
	return doc, nil
}

// CurrentLegalDocuments — действующие версии всех видов (для `maxim legal list`).
func (s *Service) CurrentLegalDocuments(ctx context.Context) ([]domain.LegalDocument, error) {
	return s.Legal.Current(ctx)
}
