package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

// Операции администратора над организациями и пользователями (этап «API администратора»,
// R-12 … R-14). ИНН, почта и телефон — полные у senior, с маской у moderator.

// OrdCounterparty — контрагент организации в ОРД (модуль marking, bootstrap); nil — маркировка
// выключена или контрагента ещё нет.
type OrdCounterparty interface {
	OrganizationCounterparty(ctx context.Context, organizationID uuid.UUID) (*apigen.OrdObject, error)
}

// SetOrdCounterparty подключает порт статуса контрагента ОРД.
func (h *Handlers) SetOrdCounterparty(p OrdCounterparty) { h.ord = p }

func (h *Handlers) ListAdminOrganizations(ctx context.Context, req apigen.ListAdminOrganizationsRequestObject) (apigen.ListAdminOrganizationsResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Account.ListAdminOrganizations", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	f := domain.AdminOrganizationFilter{Offset: pg.Offset, Limit: pg.Limit, ExactINN: !httpx.IsSenior(p)}
	if req.Params.Query != nil {
		f.Query = *req.Params.Query
	}
	if req.Params.Role != nil {
		f.Cabinet = domain.Cabinet(*req.Params.Role)
	}
	items, total, err := h.svc.AdminOrganizations(ctx, f)
	if err != nil {
		return nil, err
	}
	out := apigen.ListAdminOrganizations200JSONResponse{Items: []apigen.AdminOrganizationListItem{}, Total: &total, NextCursor: pg.Next(total)}
	for _, it := range items {
		cabs := []apigen.Cabinet{}
		if it.Advertiser {
			cabs = append(cabs, apigen.Cabinet(domain.CabinetAdvertiser))
		}
		if it.Blogger {
			cabs = append(cabs, apigen.Cabinet(domain.CabinetBlogger))
		}
		out.Items = append(out.Items, apigen.AdminOrganizationListItem{
			Id: it.ID, Name: it.Name, Inn: httpx.INNFor(p, it.INN), LegalType: apigen.LegalType(it.LegalType),
			Status: apigen.AdminOrganizationListItemStatus(it.Status), Cabinets: cabs, CreatedAt: it.CreatedAt,
		})
	}
	return out, nil
}

func (h *Handlers) GetAdminOrganization(ctx context.Context, req apigen.GetAdminOrganizationRequestObject) (apigen.GetAdminOrganizationResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Account.GetAdminOrganization", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.AdminOrganization(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	o, err := h.toAdminOrganization(ctx, p, d)
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminOrganization200JSONResponse(o), nil
}

func (h *Handlers) UpdateAdminOrganization(ctx context.Context, req apigen.UpdateAdminOrganizationRequestObject) (apigen.UpdateAdminOrganizationResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Account.UpdateAdminOrganization", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.AdminUpdateOrganization(ctx, p.UserID, req.Id, req.Body.Name, req.Body.Kpp, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	o, err := h.toAdminOrganization(ctx, p, d)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateAdminOrganization200JSONResponse(o), nil
}

func (h *Handlers) BlockUser(ctx context.Context, req apigen.BlockUserRequestObject) (apigen.BlockUserResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Account.BlockUser", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	u, err := h.svc.BlockUser(ctx, p.UserID, req.Id, reasonOf(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.BlockUser200JSONResponse(toAdminUser(p, *u)), nil
}

func (h *Handlers) UnblockUser(ctx context.Context, req apigen.UnblockUserRequestObject) (apigen.UnblockUserResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Account.UnblockUser", httpx.AdminSeniorOnly...)
	if err != nil {
		return nil, err
	}
	u, err := h.svc.UnblockUser(ctx, p.UserID, req.Id, reasonOf(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.UnblockUser200JSONResponse(toAdminUser(p, *u)), nil
}

func reasonOf(b *apigen.AdminReasonInput) string {
	if b == nil {
		return ""
	}
	return b.Reason
}

func (h *Handlers) toAdminOrganization(ctx context.Context, p *httpx.Principal, d *app.AdminOrganizationDetail) (apigen.AdminOrganization, error) {
	o := d.Organization
	out := apigen.AdminOrganization{
		Id: o.ID, LegalType: apigen.LegalType(o.LegalType), Inn: httpx.INNFor(p, o.INN), Name: o.Name,
		Kpp: o.KPP, Address: o.Address, TaxRegime: enum[apigen.TaxRegime](o.TaxRegime), VatRate: enum[apigen.VatRate](o.VatRate),
		Status: apigen.AdminOrganizationStatus(o.Status), Cabinets: cabinets(o), TaxDocumentUploaded: o.TaxDocument != nil,
		CreatedAt: o.CreatedAt, Members: []apigen.AdminUser{},
	}
	if o.ContactName != "" {
		out.ContactName = &o.ContactName
	}
	if o.Phone != "" {
		phone := httpx.PhoneFor(p, o.Phone)
		out.Phone = &phone
	}
	for _, m := range d.Members {
		out.Members = append(out.Members, toAdminUser(p, m))
	}
	if h.ord != nil {
		cp, err := h.ord.OrganizationCounterparty(ctx, o.ID)
		if err != nil {
			return apigen.AdminOrganization{}, err
		}
		out.OrdCounterparty = cp
	}
	return out, nil
}

func toAdminUser(p *httpx.Principal, u domain.User) apigen.AdminUser {
	return apigen.AdminUser{
		Id: u.ID, Email: httpx.EmailFor(p, u.Email), AdminRole: enum[apigen.AdminRole](u.AdminRole),
		Status: apigen.AdminUserStatus(u.Status), BlockedAt: u.BlockedAt, BlockedReason: u.BlockedReason,
		LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt,
	}
}
