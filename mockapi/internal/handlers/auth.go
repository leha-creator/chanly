package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// LoginCode — единственный код входа тестового сервера.
const LoginCode = "000000"

func (h *Handlers) RequestLoginCode(ctx context.Context, req api.RequestLoginCodeRequestObject) (api.RequestLoginCodeResponseObject, error) {
	// Письмо не отправляется; ответ одинаков для любого адреса (07 §4).
	h.logger(ctx).Info("login code requested", "op", "Auth.RequestCode")
	return api.RequestLoginCode202JSONResponse{Body: api.LoginCodeResponse{ExpiresInSeconds: 600}}, nil
}

func (h *Handlers) Login(ctx context.Context, req api.LoginRequestObject) (api.LoginResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	email := strings.ToLower(strings.TrimSpace(string(req.Body.Email)))
	if req.Body.Code != LoginCode {
		h.logger(ctx).Warn("login rejected: wrong code", "op", "Auth.Login")
		return nil, store.Unauthorized("wrong or expired code")
	}
	var u *store.User
	for _, x := range tx.D.Users {
		if x.Email == email {
			u = x
		}
	}
	if u == nil {
		u = &store.User{ID: store.NewID(), Email: email, CreatedAt: tx.Now}
		tx.D.Users[u.ID] = u
		h.logger(ctx).Info("user created", "op", "Auth.Login", "user_id", u.ID)
	}
	if u.Blocked {
		h.logger(ctx).Warn("login rejected: user blocked", "op", "Auth.Login", "user_id", u.ID)
		e := store.Forbidden("user is blocked")
		e.Details = map[string]any{"reason": "user_blocked"}
		return nil, e
	}
	now := tx.Now
	u.LastLoginAt = &now
	token := tx.CreateSession(u.ID)
	h.logger(ctx).Info("user logged in", "op", "Auth.Login", "user_id", u.ID)
	// Secure — только при MOCKAPI_COOKIE_SECURE=true: локально сервер работает по http.
	cookie := h.cookie(token, 30*24*3600)
	return api.Login200JSONResponse{Body: me(tx, u), Headers: api.Login200ResponseHeaders{SetCookie: &cookie}}, nil
}

func (h *Handlers) Logout(ctx context.Context, req api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	tx.DeleteSession(tokenFrom(ctx))
	h.logger(ctx).Info("user logged out", "op", "Auth.Logout", "user_id", u.ID)
	cookie := h.cookie("", 0)
	return api.Logout204Response{Headers: api.Logout204ResponseHeaders{SetCookie: &cookie}}, nil
}

func (h *Handlers) GetMe(ctx context.Context, req api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetMe200JSONResponse{Body: me(tx, u)}, nil
}

func cabinets(o *store.Organization) []api.Cabinet {
	out := []api.Cabinet{}
	if o.IsAdvertiser {
		out = append(out, "advertiser")
	}
	if o.IsBlogger {
		out = append(out, "blogger")
	}
	return out
}

// requiredDocs — документы, которые нужно принять для открытых кабинетов.
func requiredDocs(o *store.Organization) []string {
	kinds := []string{"platform_rules", "privacy_policy"}
	if o.IsAdvertiser {
		kinds = append(kinds, "advertiser_offer")
	}
	if o.IsBlogger {
		kinds = append(kinds, "blogger_offer")
	}
	return kinds
}

func me(tx *store.Tx, u *store.User) api.Me {
	m := api.Me{
		User:                    api.User{Id: u.ID, Email: openapi_types.Email(u.Email), LastLoginAt: u.LastLoginAt},
		Organizations:           []api.OrganizationBrief{},
		PendingLegalDocumentIds: []api.Uuid{},
	}
	if u.AdminRole != "" {
		m.AdminRole = ptr(api.AdminRole(u.AdminRole))
	}
	if u.OrgID == nil {
		return m
	}
	o, err := tx.Org(*u.OrgID)
	if err != nil {
		return m
	}
	m.Organizations = append(m.Organizations, api.OrganizationBrief{
		Id: o.ID, Name: o.Name, Inn: o.INN, LegalType: api.LegalType(o.LegalType), Cabinets: cabinets(o),
	})
	need := requiredDocs(o)
	for _, d := range tx.D.LegalDocs {
		if _, ok := tx.D.Acceptances[o.ID][d.ID]; !ok && slices.Contains(need, d.Kind) {
			m.PendingLegalDocumentIds = append(m.PendingLegalDocumentIds, d.ID)
		}
	}
	return m
}

// ---------------------------------------------------------------- организации

// Допустимые сочетания из 04 §2.
var taxRegimes = map[string][]string{
	"self_employed":   {"npd"},
	"sole_proprietor": {"osno", "usn", "ausn", "eshn", "psn", "npd"},
	"legal_entity":    {"osno", "usn", "ausn", "eshn"},
}

var vatRates = map[string][]string{
	"osno": {"vat_22", "none"}, "usn": {"none", "vat_5", "vat_7", "vat_22"}, "eshn": {"vat_22", "none"},
	"ausn": {"none"}, "psn": {"none"}, "npd": {"none"},
}

func checkTax(legalType string, regime *api.TaxRegime, vat *api.VatRate, kpp *string) error {
	if regime != nil && !slices.Contains(taxRegimes[legalType], string(*regime)) {
		return store.Validation("tax_regime", "tax_regime is not allowed for legal_type")
	}
	if vat != nil {
		if regime == nil {
			return store.Validation("vat_rate", "vat_rate requires tax_regime")
		}
		if !slices.Contains(vatRates[string(*regime)], string(*vat)) {
			return store.Validation("vat_rate", "vat_rate is not allowed for tax_regime")
		}
	}
	if kpp != nil && legalType != "legal_entity" {
		return store.Validation("kpp", "kpp is allowed only for legal_entity")
	}
	return nil
}

func enumPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func organization(o *store.Organization) api.Organization {
	return api.Organization{
		Id: o.ID, LegalType: api.LegalType(o.LegalType), Inn: o.INN, Name: o.Name, ContactName: o.ContactName,
		Phone: o.Phone, Kpp: o.KPP, Address: o.Address,
		TaxRegime: (*api.TaxRegime)(o.TaxRegime), VatRate: (*api.VatRate)(o.VatRate),
		SignerPosition: o.SignerPosition, SignerName: o.SignerName, SignerBasisName: o.SignerBasisName,
		SignerBasisNumber: o.SignerBasisNumber, BankAccount: o.BankAccount, BankName: o.BankName,
		BankCorrAccount: o.BankCorrAccount, BankBik: o.BankBik, BloggerFullName: o.BloggerFullName,
		Status: api.OrganizationStatus(o.Status), Cabinets: cabinets(o), TaxDocumentUploaded: o.TaxDocUploaded, CreatedAt: o.CreatedAt,
	}
}

func (h *Handlers) CreateOrganization(ctx context.Context, req api.CreateOrganizationRequestObject) (api.CreateOrganizationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	log := h.logger(ctx).With("op", "Organization.Create", "user_id", u.ID, "cabinet", b.Cabinet)
	if u.OrgID != nil {
		o, err := tx.Org(*u.OrgID)
		if err != nil {
			return nil, err
		}
		if (b.Cabinet == "advertiser" && o.IsAdvertiser) || (b.Cabinet == "blogger" && o.IsBlogger) {
			return nil, store.Conflict("conflict", "cabinet is already open", map[string]any{"cabinet": b.Cabinet})
		}
		if b.Cabinet == "advertiser" {
			o.IsAdvertiser = true
		} else {
			o.IsBlogger = true
			if b.BloggerFullName != nil {
				o.BloggerFullName = b.BloggerFullName
			}
		}
		log.Info("cabinet added to organization", "organization_id", o.ID)
		return api.CreateOrganization201JSONResponse(organization(o)), nil
	}
	wantLen := 12
	if b.LegalType == "legal_entity" {
		wantLen = 10
	}
	if len(b.Inn) != wantLen {
		return nil, store.Validation("inn", fmt.Sprintf("inn must have %d digits for %s", wantLen, b.LegalType))
	}
	if err := checkTax(string(b.LegalType), b.TaxRegime, b.VatRate, b.Kpp); err != nil {
		return nil, err
	}
	for _, x := range tx.D.Orgs {
		if x.INN == b.Inn {
			log.Warn("[FIX] organization rejected: inn already registered", "code", "inn_taken")
			return nil, store.Conflict("inn_taken", "organization with this inn already exists", map[string]any{"field": "inn"})
		}
	}
	o := &store.Organization{
		ID: store.NewID(), LegalType: string(b.LegalType), INN: b.Inn, Name: b.Name, ContactName: b.ContactName,
		Phone: b.Phone, KPP: b.Kpp, Address: b.Address, TaxRegime: enumPtr(b.TaxRegime), VatRate: enumPtr(b.VatRate),
		SignerPosition: b.SignerPosition, SignerName: b.SignerName, SignerBasisName: b.SignerBasisName,
		SignerBasisNumber: b.SignerBasisNumber, BankAccount: b.BankAccount, BankName: b.BankName,
		BankCorrAccount: b.BankCorrAccount, BankBik: b.BankBik, BloggerFullName: b.BloggerFullName,
		Status: "active", IsAdvertiser: b.Cabinet == "advertiser", IsBlogger: b.Cabinet == "blogger", CreatedAt: tx.Now,
	}
	tx.D.Orgs[o.ID] = o
	u.OrgID = &o.ID
	if o.IsBlogger {
		tx.D.Rocketwork[o.ID] = &store.RocketworkContractor{Status: "none"}
	}
	log.Info("organization created", "organization_id", o.ID)
	return api.CreateOrganization201JSONResponse(organization(o)), nil
}

func (h *Handlers) GetCurrentOrganization(ctx context.Context, req api.GetCurrentOrganizationRequestObject) (api.GetCurrentOrganizationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := org(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetCurrentOrganization200JSONResponse(organization(o)), nil
}

func (h *Handlers) UpdateCurrentOrganization(ctx context.Context, req api.UpdateCurrentOrganizationRequestObject) (api.UpdateCurrentOrganizationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := org(ctx, tx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	regime := b.TaxRegime
	if regime == nil && o.TaxRegime != nil {
		regime = ptr(api.TaxRegime(*o.TaxRegime))
	}
	vat := b.VatRate
	if vat == nil && b.TaxRegime == nil && o.VatRate != nil {
		vat = ptr(api.VatRate(*o.VatRate))
	}
	if err := checkTax(o.LegalType, regime, vat, b.Kpp); err != nil {
		return nil, err
	}
	set := func(dst **string, v *string) {
		if v != nil {
			*dst = v
		}
	}
	if b.Name != nil {
		o.Name = *b.Name
	}
	if b.ContactName != nil {
		o.ContactName = *b.ContactName
	}
	if b.Phone != nil {
		o.Phone = *b.Phone
	}
	set(&o.KPP, b.Kpp)
	set(&o.Address, b.Address)
	set(&o.SignerPosition, b.SignerPosition)
	set(&o.SignerName, b.SignerName)
	set(&o.SignerBasisName, b.SignerBasisName)
	set(&o.SignerBasisNumber, b.SignerBasisNumber)
	set(&o.BankAccount, b.BankAccount)
	set(&o.BankName, b.BankName)
	set(&o.BankCorrAccount, b.BankCorrAccount)
	set(&o.BankBik, b.BankBik)
	set(&o.BloggerFullName, b.BloggerFullName)
	o.TaxRegime, o.VatRate = enumPtr(regime), enumPtr(vat)
	h.logger(ctx).Info("organization updated", "op", "Organization.Update", "organization_id", o.ID)
	return api.UpdateCurrentOrganization200JSONResponse(organization(o)), nil
}

const maxTaxDocument = 10 << 20

// UploadTaxDocument проверяет тип и размер файла и запоминает только признак загрузки.
func (h *Handlers) UploadTaxDocument(ctx context.Context, req api.UploadTaxDocumentRequestObject) (api.UploadTaxDocumentResponseObject, error) {
	h.logger(ctx).Debug("tax document upload started", "op", "Organization.UploadTaxDocument")
	var data []byte
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, store.Validation("file", "malformed multipart body")
		}
		if part.FormName() != "file" {
			continue
		}
		if data, err = io.ReadAll(io.LimitReader(part, maxTaxDocument+1)); err != nil {
			return nil, store.Validation("file", "cannot read file")
		}
	}
	if data == nil {
		return nil, store.Validation("file", "file part is missing")
	}
	if len(data) > maxTaxDocument {
		return nil, store.Validation("file", "file is larger than 10 MB")
	}
	mime := http.DetectContentType(data) // тип — по содержимому, не по расширению
	if !slices.Contains([]string{"application/pdf", "image/jpeg", "image/png"}, mime) {
		return nil, store.Validation("file", "only PDF, JPEG and PNG are accepted")
	}

	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	o.TaxDocUploaded = true
	h.logger(ctx).Info("tax document uploaded", "op", "Organization.UploadTaxDocument",
		"organization_id", o.ID, "size", len(data), "content_type", mime)
	return api.UploadTaxDocument200JSONResponse(organization(o)), nil
}

// ---------------------------------------------------------------- документы и уведомления

func (h *Handlers) ListLegalDocuments(ctx context.Context, req api.ListLegalDocumentsRequestObject) (api.ListLegalDocumentsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	items := []api.LegalDocument{}
	for _, d := range tx.D.LegalDocs {
		accepted := false
		if u.OrgID != nil {
			_, accepted = tx.D.Acceptances[*u.OrgID][d.ID]
		}
		items = append(items, api.LegalDocument{Id: d.ID, Kind: api.LegalDocumentKind(d.Kind), Version: d.Version, PublishedAt: d.PublishedAt,
			Title: d.Title, Url: d.URL, Accepted: accepted})
	}
	return api.ListLegalDocuments200JSONResponse{Items: items}, nil
}

func (h *Handlers) AcceptLegalDocuments(ctx context.Context, req api.AcceptLegalDocumentsRequestObject) (api.AcceptLegalDocumentsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, o, err := org(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, id := range req.Body.LegalDocumentIds {
		if !slices.ContainsFunc(tx.D.LegalDocs, func(d *store.LegalDocument) bool { return d.ID == id }) {
			return nil, store.NotFound("legal_document", id)
		}
	}
	if tx.D.Acceptances[o.ID] == nil {
		tx.D.Acceptances[o.ID] = map[api.Uuid]time.Time{}
	}
	for _, id := range req.Body.LegalDocumentIds {
		tx.D.Acceptances[o.ID][id] = tx.Now
	}
	h.logger(ctx).Info("legal documents accepted", "op", "Legal.Accept", "organization_id", o.ID, "user_id", u.ID,
		"count", len(req.Body.LegalDocumentIds))
	return api.AcceptLegalDocuments204Response{}, nil
}

func (h *Handlers) ListNotifications(ctx context.Context, req api.ListNotificationsRequestObject) (api.ListNotificationsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.Notification
	for _, n := range tx.D.Notifications {
		if n.UserID == u.ID && (req.Params.Unread == nil || !*req.Params.Unread || n.ReadAt == nil) {
			mine = append(mine, n)
		}
	}
	mine = newestFirst(mine, func(n *store.Notification) time.Time { return n.CreatedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.Notification{}
	for _, n := range pg {
		items = append(items, mockNotification(n))
	}
	return api.ListNotifications200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) MarkAllNotificationsRead(ctx context.Context, _ api.MarkAllNotificationsReadRequestObject) (api.MarkAllNotificationsReadResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	marked := 0
	for _, n := range tx.D.Notifications {
		if n.UserID == u.ID && n.ReadAt == nil {
			now := tx.Now
			n.ReadAt = &now
			marked++
		}
	}
	h.logger(ctx).Info("[FIX] notifications marked read", "op", "Notification.ReadAll", "user_id", u.ID, "count", marked)
	return api.MarkAllNotificationsRead204Response{}, nil
}

func (h *Handlers) MarkNotificationRead(ctx context.Context, req api.MarkNotificationReadRequestObject) (api.MarkNotificationReadResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := user(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, n := range tx.D.Notifications {
		if n.ID == req.Id && n.UserID == u.ID {
			if n.ReadAt == nil {
				now := tx.Now
				n.ReadAt = &now
			}
			return api.MarkNotificationRead204Response{}, nil
		}
	}
	return nil, store.NotFound("notification", req.Id)
}
