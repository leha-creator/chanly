package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ObjectView — объект ОРД для администратора (этап «API администратора», R-17): состояние
// синхронизации и последний вызов ОРД. Роль проверяет вызывающий.
type ObjectView struct {
	Ref       domain.ObjectRef
	Sync      domain.SyncState
	Title     string
	Erid      string
	UpdatedAt time.Time
	// LastRequest — последний вызов ОРД; nil — вызовов не было.
	LastRequest *domain.RequestLog
}

// Object — состояние объекта и последний вызов ОРД; нет объекта — 404.
func (s *Service) Object(ctx context.Context, ref domain.ObjectRef) (*ObjectView, error) {
	const op = "Marking.Object"
	if !ref.Type.Valid() {
		return nil, apperr.NotFound("ord object not found")
	}
	var view *ObjectView
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		r, err := s.lock(ctx, ref)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("ord object not found")
		}
		if err != nil {
			return err
		}
		view, err = s.view(ctx, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.DebugContext(ctx, "ord object read", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID,
		"has_request", view.LastRequest != nil)
	return view, nil
}

// OrganizationObject — контрагент организации кабинета в ОРД; nil — его ещё нет.
func (s *Service) OrganizationObject(ctx context.Context, organizationID uuid.UUID) (*ObjectView, error) {
	o, err := s.Organizations.ByOrganization(ctx, organizationID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Object(ctx, o.Ref())
}

// VersionObject — креатив версии поста в ОРД; nil — его ещё нет.
func (s *Service) VersionObject(ctx context.Context, versionID uuid.UUID) (*ObjectView, error) {
	c, err := s.Creatives.ByVersion(ctx, versionID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Object(ctx, c.Ref())
}

func (s *Service) view(ctx context.Context, r record) (*ObjectView, error) {
	v := &ObjectView{Ref: r.ref(), Sync: *r.state()}
	switch x := r.(type) {
	case *orgRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = "Контрагент " + x.v.OrganizationID.String()
		if d, err := s.Accounts.Details(ctx, x.v.OrganizationID); err == nil {
			v.Title = d.Name
		} else {
			s.log.DebugContext(ctx, "ord object title unavailable", KeyObjectID, x.v.ID, logx.KeyError, err.Error())
		}
	case *contractRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = "Договор " + string(x.v.Kind)
		if x.v.Number != "" {
			v.Title = "Договор № " + x.v.Number
		}
	case *platformRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = x.v.Name
	case *creativeRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Erid = x.v.Erid
		v.Title = "Креатив версии " + x.v.PostVersionID.String()
	case *statisticsRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = "Статистика " + x.v.Period.String()
	case *invoiceRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = "Акт № " + x.v.Number
	case *invoiceLinksRecord:
		v.UpdatedAt = x.v.UpdatedAt
		v.Title = "Креативы акта № " + x.v.Number
	}
	last, err := s.Overview.LastRequest(ctx, v.Ref, r.externalID())
	if err != nil {
		return nil, err
	}
	v.LastRequest = last
	return v, nil
}
