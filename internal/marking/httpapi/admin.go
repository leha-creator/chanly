// Package httpapi — операции OpenAPI модуля маркировки для администратора (этап «API
// администратора», S-20, R-17): ошибки ОРД, объект и его последний вызов, повтор отправки.
// Преобразование apigen ↔ domain — только здесь.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Handlers реализует операции модуля из apigen.StrictServerInterface.
type Handlers struct {
	svc  *app.Service
	refs adminref.Directory
	log  *slog.Logger
}

// New создаёт обработчики.
func New(svc *app.Service, refs adminref.Directory, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, refs: refs, log: log.With(logx.KeyModule, "marking")}
}

func (h *Handlers) ListOrdErrors(ctx context.Context, req apigen.ListOrdErrorsRequestObject) (apigen.ListOrdErrorsResponseObject, error) {
	ctx, _, err := httpx.AdminContext(ctx, h.log, "Marking.ListOrdErrors", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	var typ domain.ObjectType
	if req.Params.Type != nil {
		typ = domain.ObjectType(*req.Params.Type)
	}
	cursor, limit := "", 0
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	page, err := h.svc.ListErrorsOfType(ctx, typ, cursor, limit)
	if err != nil {
		return nil, err
	}
	out := apigen.ListOrdErrors200JSONResponse{Items: []apigen.OrdObject{}}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	for _, it := range page.Items {
		obj := apigen.OrdObject{
			Id: it.Ref.ID, Type: apigen.OrdObjectType(it.Ref.Type), Status: apigen.OrdSyncStatusError,
			Title: titleOf(it.Ref), Attempts: it.Attempts, UpdatedAt: it.UpdatedAt,
			Error: opt(it.Error), OrdStatus: opt(it.OrdStatus), ExternalId: opt(it.ExternalID),
		}
		out.Items = append(out.Items, obj)
	}
	return out, nil
}

func (h *Handlers) GetOrdObject(ctx context.Context, req apigen.GetOrdObjectRequestObject) (apigen.GetOrdObjectResponseObject, error) {
	ctx, _, err := httpx.AdminContext(ctx, h.log, "Marking.GetOrdObject", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Object(ctx, domain.ObjectRef{Type: domain.ObjectType(req.Type), ID: req.Id})
	if err != nil {
		return nil, err
	}
	return apigen.GetOrdObject200JSONResponse(toObject(v)), nil
}

func (h *Handlers) RetryOrdObject(ctx context.Context, req apigen.RetryOrdObjectRequestObject) (apigen.RetryOrdObjectResponseObject, error) {
	ctx, p, err := httpx.AdminContext(ctx, h.log, "Marking.RetryOrdObject", httpx.AdminAny...)
	if err != nil {
		return nil, err
	}
	ref := domain.ObjectRef{Type: domain.ObjectType(req.Type), ID: req.Id}
	if !ref.Type.Valid() {
		return nil, apperr.NotFound("ord object not found")
	}
	reason := ""
	if req.Body != nil {
		reason = req.Body.Reason
	}
	if err := h.svc.Retry(ctx, ref, p.UserID, reason); err != nil {
		return nil, err
	}
	v, err := h.svc.Object(ctx, ref)
	if err != nil {
		return nil, err
	}
	return apigen.RetryOrdObject200JSONResponse(toObject(v)), nil
}

// OrganizationCounterparty — порт account/httpapi.OrdCounterparty: контрагент в карточке организации.
func (h *Handlers) OrganizationCounterparty(ctx context.Context, organizationID uuid.UUID) (*apigen.OrdObject, error) {
	v, err := h.svc.OrganizationObject(ctx, organizationID)
	if err != nil || v == nil {
		return nil, err
	}
	o := toObject(v)
	return &o, nil
}

// VersionCreative — порт placement/httpapi.AdminCreatives: креатив версии в карточке поста.
func (h *Handlers) VersionCreative(ctx context.Context, versionID uuid.UUID) (*apigen.OrdObject, error) {
	v, err := h.svc.VersionObject(ctx, versionID)
	if err != nil || v == nil {
		return nil, err
	}
	o := toObject(v)
	return &o, nil
}

func toObject(v *app.ObjectView) apigen.OrdObject {
	out := apigen.OrdObject{
		Id: v.Ref.ID, Type: apigen.OrdObjectType(v.Ref.Type), Status: apigen.OrdSyncStatus(v.Sync.Status),
		Title: v.Title, Attempts: v.Sync.Attempts, UpdatedAt: v.UpdatedAt,
		Error: opt(v.Sync.Error), OrdStatus: opt(v.Sync.OrdStatus), ExternalId: opt(v.Sync.ExternalID), Erid: opt(v.Erid),
	}
	if r := v.LastRequest; r != nil {
		out.LastRequest = &apigen.OrdRequestLog{
			Id: r.ID, Operation: r.Operation, Attempt: r.Attempt, CreatedAt: r.CreatedAt, Error: opt(r.Error),
			ResponseStatus: r.ResponseStatus, Request: body(r.Request), Response: body(r.Response),
		}
	}
	return out
}

// titleOf — подпись строки списка без чтения объекта: вид и идентификатор.
func titleOf(ref domain.ObjectRef) string { return ref.String() }

// body — сохранённое тело вызова как объект; не объект (массив, пусто) — под ключом value.
func body(raw []byte) *map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		return &m
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &map[string]any{"value": v}
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
