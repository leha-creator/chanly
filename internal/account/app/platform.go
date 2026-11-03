package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// PlatformOrg — реквизиты платформы из конфигурации (ORD_PLATFORM_INN, ORD_PLATFORM_NAME,
// ORD_PLATFORM_LEGAL_TYPE; этап «Маркировка», R-5).
type PlatformOrg struct {
	INN       string
	Name      string
	LegalType domain.LegalType
}

// ErrPlatformConflict — реквизиты платформы в конфигурации не совпадают с базой: запуск невозможен.
var ErrPlatformConflict = errors.New("platform organization conflicts with configuration")

// SetOrganizationListener подключает модуль маркировки (R-15); nil — отключить.
func (s *Service) SetOrganizationListener(l domain.OrganizationListener) {
	if l == nil {
		l = domain.NopOrganizationListener{}
	}
	s.orgListener = l
}

func (s *Service) organizationListener() domain.OrganizationListener {
	if s.orgListener == nil {
		return domain.NopOrganizationListener{}
	}
	return s.orgListener
}

// EnsurePlatformOrganization обеспечивает строку организации-платформы при запуске api и worker
// (R-5): нет строки — вставка; есть с тем же ИНН — обновление наименования. ИНН занят обычной
// организацией, у строки платформы другой ИНН или тип лица — ErrPlatformConflict.
func (s *Service) EnsurePlatformOrganization(ctx context.Context, in PlatformOrg) (uuid.UUID, error) {
	const op = "Account.EnsurePlatformOrganization"
	in.Name = strings.TrimSpace(in.Name)
	if !in.LegalType.Valid() || in.Name == "" {
		return uuid.Nil, fmt.Errorf("%w: legal type and name are required", ErrPlatformConflict)
	}
	if reason := domain.CheckINN(in.INN, in.LegalType); reason != "" {
		return uuid.Nil, fmt.Errorf("%w: inn %s", ErrPlatformConflict, reason)
	}
	if s.Platform == nil {
		return uuid.Nil, errors.New("platform organizations repository is not configured")
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorSystem})
	var (
		id      uuid.UUID
		created bool
		renamed bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		p, err := s.Platform.Platform(ctx)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			p = &domain.PlatformOrganization{ID: ids.New(), LegalType: in.LegalType, INN: in.INN, Name: in.Name}
			if err := s.Platform.InsertPlatform(ctx, *p, now); err != nil {
				if apperr.Is(err, apperr.CodeINNTaken) {
					return fmt.Errorf("%w: inn belongs to a regular organization", ErrPlatformConflict)
				}
				return err
			}
			id, created = p.ID, true
			return s.Audit.Record(ctx, audit.Entry{
				Action: "organization.platform_created", ObjectType: "organization", ObjectID: p.ID.String(),
				Data: map[string]any{"legal_type": string(in.LegalType), "inn": logx.MaskINN(in.INN)},
			})
		case err != nil:
			return err
		}
		id = p.ID
		if p.INN != in.INN || p.LegalType != in.LegalType {
			return fmt.Errorf("%w: platform row has another inn or legal type", ErrPlatformConflict)
		}
		if p.Name == in.Name {
			return nil
		}
		if err := s.Platform.RenamePlatform(ctx, p.ID, in.Name, now); err != nil {
			return err
		}
		renamed = true
		return s.Audit.Record(ctx, audit.Entry{
			Action: "organization.platform_renamed", ObjectType: "organization", ObjectID: p.ID.String(),
			Data: map[string]any{"changed": []string{"name"}},
		})
	})
	if err != nil {
		s.log.ErrorContext(ctx, "platform organization ensure failed", logx.KeyOp, op,
			"inn", logx.MaskINN(in.INN), logx.KeyError, err.Error())
		return uuid.Nil, err
	}
	s.log.InfoContext(ctx, "platform organization ensured", logx.KeyOp, op, logx.KeyOrganizationID, id,
		"created", created, "renamed", renamed, "inn", logx.MaskINN(in.INN))
	return id, nil
}
