// Package httpapi — обработчики операций OpenAPI модуля аккаунтов (теги auth,
// organizations, legal). Преобразование apigen ↔ domain — только здесь.
package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Options — параметры cookie.
type Options struct {
	// CookieSecure — признак Secure у cookie (обязателен в продакшене).
	CookieSecure bool
}

// Handlers реализует операции модуля из apigen.StrictServerInterface.
type Handlers struct {
	svc  *app.Service
	opts Options
	log  *slog.Logger
	// ord — статус контрагента ОРД в карточке организации администратора; nil — не подключён.
	ord OrdCounterparty
}

// New создаёт обработчики.
func New(svc *app.Service, opts Options, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, opts: opts, log: log.With(logx.KeyModule, "account")}
}

// Authenticate — проверка сессии для httpx.Session.
func (h *Handlers) Authenticate(ctx context.Context, token string) (*httpx.Principal, error) {
	p, err := h.svc.Authenticate(ctx, token)
	return toHTTPPrincipal(p, err)
}

// AuthenticateStream deliberately does not renew the cookie session.
func (h *Handlers) AuthenticateStream(ctx context.Context, token string) (*httpx.Principal, error) {
	p, err := h.svc.AuthenticateStream(ctx, token)
	return toHTTPPrincipal(p, err)
}

func toHTTPPrincipal(p *app.Principal, err error) (*httpx.Principal, error) {
	if err != nil {
		return nil, err
	}
	out := &httpx.Principal{UserID: p.UserID, SessionID: p.SessionID, OrganizationID: p.OrganizationID}
	for _, c := range p.Cabinets {
		out.Cabinets = append(out.Cabinets, string(c))
	}
	if p.AdminRole != nil {
		out.AdminRole = string(*p.AdminRole)
	}
	return out, nil
}

// principal переводит пользователя запроса в принципала сервиса.
func principal(ctx context.Context) (app.Principal, error) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok {
		return app.Principal{}, apperr.Unauthorized("session is missing")
	}
	out := app.Principal{UserID: p.UserID, SessionID: p.SessionID, OrganizationID: p.OrganizationID}
	for _, c := range p.Cabinets {
		out.Cabinets = append(out.Cabinets, domain.Cabinet(c))
	}
	if p.AdminRole != "" {
		r := domain.AdminRole(p.AdminRole)
		out.AdminRole = &r
	}
	return out, nil
}

// ---------------------------------------------------------------- auth

func (h *Handlers) RequestLoginCode(ctx context.Context, req apigen.RequestLoginCodeRequestObject) (apigen.RequestLoginCodeResponseObject, error) {
	ttl, err := h.svc.RequestLoginCode(ctx, string(req.Body.Email), httpx.ClientIP(ctx))
	if err != nil {
		return nil, err
	}
	return apigen.RequestLoginCode202JSONResponse{
		Body:    apigen.LoginCodeResponse{ExpiresInSeconds: int(ttl.Seconds())},
		Headers: apigen.RequestLoginCode202ResponseHeaders{XRequestId: requestID(ctx)},
	}, nil
}

func (h *Handlers) Login(ctx context.Context, req apigen.LoginRequestObject) (apigen.LoginResponseObject, error) {
	r, err := h.svc.Login(ctx, string(req.Body.Email), req.Body.Code, httpx.ClientIP(ctx), httpx.UserAgent(ctx))
	if err != nil {
		return nil, err
	}
	me, err := h.svc.GetMe(ctx, r.Principal)
	if err != nil {
		return nil, err
	}
	maxAge := int(r.SessionTTL.Seconds())
	return loginResponse{
		body:      toMe(me),
		requestID: logx.RequestID(ctx),
		cookies: []*http.Cookie{
			h.cookie(httpx.SessionCookie, r.SessionToken, maxAge, true),
			h.cookie(httpx.CSRFCookie, r.CSRFToken, maxAge, false),
		},
	}, nil
}

func (h *Handlers) Logout(ctx context.Context, _ apigen.LogoutRequestObject) (apigen.LogoutResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Logout(ctx, p); err != nil {
		return nil, err
	}
	return logoutResponse{cookies: []*http.Cookie{
		h.cookie(httpx.SessionCookie, "", -1, true),
		h.cookie(httpx.CSRFCookie, "", -1, false),
	}}, nil
}

func (h *Handlers) GetMe(ctx context.Context, _ apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	me, err := h.svc.GetMe(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.GetMe200JSONResponse{Body: toMe(me), Headers: apigen.GetMe200ResponseHeaders{XRequestId: requestID(ctx)}}, nil
}

// cookie собирает cookie сессии или CSRF (07 §4, R-4). maxAge < 0 удаляет cookie.
func (h *Handlers) cookie(name, value string, maxAge int, httpOnly bool) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: httpOnly, Secure: h.opts.CookieSecure, SameSite: http.SameSiteLaxMode,
	}
}

// loginResponse — ответ входа с двумя cookie: сгенерированный тип допускает только одну.
type loginResponse struct {
	body      apigen.Me
	requestID string
	cookies   []*http.Cookie
}

func (r loginResponse) VisitLoginResponse(w http.ResponseWriter) error {
	for _, c := range r.cookies {
		http.SetCookie(w, c)
	}
	w.Header().Set("X-Request-Id", r.requestID)
	httpx.WriteJSON(w, http.StatusOK, r.body)
	return nil
}

type logoutResponse struct{ cookies []*http.Cookie }

func (r logoutResponse) VisitLogoutResponse(w http.ResponseWriter) error {
	for _, c := range r.cookies {
		http.SetCookie(w, c)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------------------------------------------------------------- organizations

func (h *Handlers) CreateOrganization(ctx context.Context, req apigen.CreateOrganizationRequestObject) (apigen.CreateOrganizationResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := domain.NewOrganizationInput{
		Cabinet: domain.Cabinet(b.Cabinet), LegalType: domain.LegalType(b.LegalType), INN: b.Inn,
		Fields: domain.Fields{
			Name: &b.Name, ContactName: &b.ContactName, Phone: &b.Phone, KPP: b.Kpp, Address: b.Address,
			TaxRegime: enum[domain.TaxRegime](b.TaxRegime), VatRate: enum[domain.VatRate](b.VatRate),
			SignerPosition: b.SignerPosition, SignerName: b.SignerName, SignerBasisName: b.SignerBasisName,
			SignerBasisNumber: b.SignerBasisNumber, BankAccount: b.BankAccount, BankName: b.BankName,
			BankCorrAccount: b.BankCorrAccount, BankBIK: b.BankBik, BloggerFullName: b.BloggerFullName,
		},
	}
	o, err := h.svc.CreateOrganization(ctx, p, in)
	if err != nil {
		return nil, err
	}
	return apigen.CreateOrganization201JSONResponse(toOrganization(o)), nil
}

func (h *Handlers) GetCurrentOrganization(ctx context.Context, _ apigen.GetCurrentOrganizationRequestObject) (apigen.GetCurrentOrganizationResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	o, err := h.svc.GetOrganization(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.GetCurrentOrganization200JSONResponse(toOrganization(o)), nil
}

func (h *Handlers) UpdateCurrentOrganization(ctx context.Context, req apigen.UpdateCurrentOrganizationRequestObject) (apigen.UpdateCurrentOrganizationResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	f := domain.Fields{
		Name: b.Name, ContactName: b.ContactName, Phone: b.Phone, KPP: b.Kpp, Address: b.Address,
		TaxRegime: enum[domain.TaxRegime](b.TaxRegime), VatRate: enum[domain.VatRate](b.VatRate),
		SignerPosition: b.SignerPosition, SignerName: b.SignerName, SignerBasisName: b.SignerBasisName,
		SignerBasisNumber: b.SignerBasisNumber, BankAccount: b.BankAccount, BankName: b.BankName,
		BankCorrAccount: b.BankCorrAccount, BankBIK: b.BankBik, BloggerFullName: b.BloggerFullName,
	}
	o, err := h.svc.UpdateOrganization(ctx, p, f)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateCurrentOrganization200JSONResponse(toOrganization(o)), nil
}

func (h *Handlers) UploadTaxDocument(ctx context.Context, req apigen.UploadTaxDocumentRequestObject) (apigen.UploadTaxDocumentResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	data, err := readFilePart(req)
	if err != nil {
		return nil, err
	}
	o, err := h.svc.UploadTaxDocument(ctx, p, data)
	if err != nil {
		return nil, err
	}
	return apigen.UploadTaxDocument200JSONResponse(toOrganization(o)), nil
}

// readFilePart читает часть file из multipart, не больше предела + 1 байт.
func readFilePart(req apigen.UploadTaxDocumentRequestObject) ([]byte, error) {
	var data []byte
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperr.Validation("file", "malformed").WithCause(err)
		}
		if part.FormName() != "file" {
			continue
		}
		if data, err = io.ReadAll(io.LimitReader(part, app.MaxTaxDocumentSize+1)); err != nil {
			return nil, apperr.Validation("file", "too_large").WithCause(err)
		}
	}
	if data == nil {
		return nil, apperr.Validation("file", domain.ReasonRequired)
	}
	return data, nil
}

// ---------------------------------------------------------------- legal

func (h *Handlers) ListLegalDocuments(ctx context.Context, _ apigen.ListLegalDocumentsRequestObject) (apigen.ListLegalDocumentsResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	views, err := h.svc.ListLegalDocuments(ctx, p)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.LegalDocument, 0, len(views))
	for _, v := range views {
		d := v.Document
		items = append(items, apigen.LegalDocument{
			Id: d.ID, Kind: apigen.LegalDocumentKind(d.Kind), Version: d.Version, PublishedAt: d.PublishedAt,
			Title: d.Kind.Title(), Url: v.URL, Accepted: v.Accepted,
		})
	}
	return apigen.ListLegalDocuments200JSONResponse{Items: items}, nil
}

func (h *Handlers) AcceptLegalDocuments(ctx context.Context, req apigen.AcceptLegalDocumentsRequestObject) (apigen.AcceptLegalDocumentsResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.AcceptLegalDocuments(ctx, p, req.Body.LegalDocumentIds, httpx.ClientIP(ctx)); err != nil {
		return nil, err
	}
	return apigen.AcceptLegalDocuments204Response{}, nil
}

// ---------------------------------------------------------------- преобразования

func toMe(me *app.Me) apigen.Me {
	out := apigen.Me{
		User:                    apigen.User{Id: me.User.ID, Email: openapi_types.Email(me.User.Email), LastLoginAt: me.User.LastLoginAt},
		Organizations:           []apigen.OrganizationBrief{},
		PendingLegalDocumentIds: []uuid.UUID{},
		AdminRole:               enum[apigen.AdminRole](me.User.AdminRole),
	}
	for _, o := range me.Organizations {
		out.Organizations = append(out.Organizations, apigen.OrganizationBrief{
			Id: o.ID, Name: o.Name, Inn: o.INN, LegalType: apigen.LegalType(o.LegalType), Cabinets: cabinets(o),
		})
	}
	for _, d := range me.PendingDocuments {
		out.PendingLegalDocumentIds = append(out.PendingLegalDocumentIds, d.ID)
	}
	return out
}

func toOrganization(o *domain.Organization) apigen.Organization {
	out := apigen.Organization{
		Id: o.ID, LegalType: apigen.LegalType(o.LegalType), Inn: o.INN, Name: o.Name, ContactName: o.ContactName,
		Phone: o.Phone, Address: o.Address, Kpp: o.KPP,
		TaxRegime: enum[apigen.TaxRegime](o.TaxRegime), VatRate: enum[apigen.VatRate](o.VatRate),
		SignerPosition: o.SignerPosition, SignerName: o.SignerName, SignerBasisName: o.SignerBasisName,
		SignerBasisNumber: o.SignerBasisNumber, BankAccount: o.BankAccount, BankName: o.BankName,
		BankCorrAccount: o.BankCorrAccount, BankBik: o.BankBIK, Status: apigen.OrganizationStatus(o.Status),
		Cabinets: cabinets(o), TaxDocumentUploaded: o.TaxDocument != nil, CreatedAt: o.CreatedAt,
	}
	if o.Blogger != nil {
		out.BloggerFullName = o.Blogger.FullName
	}
	return out
}

func cabinets(o *domain.Organization) []apigen.Cabinet {
	out := []apigen.Cabinet{}
	for _, c := range o.Cabinets() {
		out = append(out, apigen.Cabinet(c))
	}
	return out
}

func enum[T ~string, F ~string](v *F) *T {
	if v == nil {
		return nil
	}
	t := T(*v)
	return &t
}

func requestID(ctx context.Context) *string {
	id := logx.RequestID(ctx)
	return &id
}
