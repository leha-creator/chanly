package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func advertiserBody() map[string]any {
	in := accounttest.AdvertiserLegalEntity()
	return map[string]any{
		"cabinet": "advertiser", "legal_type": "legal_entity", "inn": in.INN, "name": *in.Fields.Name,
		"contact_name": *in.Fields.ContactName, "phone": *in.Fields.Phone, "address": *in.Fields.Address,
	}
}

func TestLoginSetsCookies(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	if r := c.do(http.MethodPost, "/auth/code", map[string]string{"email": "c@example.ru"}); r.status != http.StatusAccepted {
		t.Fatalf("code: %d", r.status)
	}
	_, _ = a.worker.RunOnce(t.Context())
	msg, _ := a.mail.Last("c@example.ru")
	r := c.do(http.MethodPost, "/auth/login", map[string]string{"email": "c@example.ru", "code": msg.Code})
	if r.status != http.StatusOK {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	cookies := map[string]*http.Cookie{}
	for _, ck := range (&http.Response{Header: r.header}).Cookies() {
		cookies[ck.Name] = ck
	}
	s, csrf := cookies[httpx.SessionCookie], cookies[httpx.CSRFCookie]
	if s == nil || !s.HttpOnly || s.SameSite != http.SameSiteLaxMode || s.Path != "/" || s.MaxAge != 30*24*3600 {
		t.Fatalf("session cookie: %+v", s)
	}
	if csrf == nil || csrf.HttpOnly || csrf.Value == "" || csrf.Value == s.Value {
		t.Fatalf("csrf cookie: %+v", csrf)
	}
	if strings.Contains(r.header.Get("Set-Cookie"), "Secure") {
		t.Fatal("Secure set although COOKIE_SECURE=false")
	}

	r = c.do(http.MethodGet, "/me", nil)
	var me struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
		Organizations []any `json:"organizations"`
	}
	r.json(t, &me)
	if r.status != http.StatusOK || me.User.Email != "c@example.ru" || len(me.Organizations) != 0 {
		t.Fatalf("me: %d %s", r.status, r.body)
	}

	if r = c.do(http.MethodPost, "/auth/logout", nil); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", r.status, r.body)
	}
	if r = c.do(http.MethodGet, "/me", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", r.status)
	}
}

func TestWrongCodeAndRateLimit(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	r := c.do(http.MethodPost, "/auth/login", map[string]string{"email": "x@example.ru", "code": "123456"})
	if code, _ := r.errorCode(t); r.status != http.StatusUnauthorized || code != "unauthorized" {
		t.Fatalf("wrong code: %d %s", r.status, r.body)
	}
	for range 5 {
		c.do(http.MethodPost, "/auth/code", map[string]string{"email": "x@example.ru"})
	}
	r = c.do(http.MethodPost, "/auth/code", map[string]string{"email": "x@example.ru"})
	if code, details := r.errorCode(t); r.status != http.StatusTooManyRequests || code != "rate_limited" || details["scope"] != "email" {
		t.Fatalf("rate limit: %d %s", r.status, r.body)
	}
	if r.header.Get("Retry-After") != "3600" {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
}

func TestSessionAndCSRFRequired(t *testing.T) {
	a := newTestApp(t)
	anon := a.client()
	if r := anon.do(http.MethodGet, "/organizations/current", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.status)
	}
	c := a.client()
	c.login("csrf@example.ru")
	c.csrf = false
	r := c.do(http.MethodPost, "/organizations", advertiserBody())
	if code, details := r.errorCode(t); r.status != http.StatusForbidden || code != "forbidden" || details["reason"] != "csrf" {
		t.Fatalf("no csrf: %d %s", r.status, r.body)
	}
	c.csrf = true
	if r = c.do(http.MethodPost, "/organizations", advertiserBody()); r.status != http.StatusCreated {
		t.Fatalf("with csrf: %d %s", r.status, r.body)
	}
}

func TestOrganizationHandlers(t *testing.T) {
	a := newTestApp(t)
	docs := a.publishAll()
	c := a.client()
	c.login("org@example.ru")
	if r := c.do(http.MethodGet, "/organizations/current", nil); r.status != http.StatusNotFound {
		t.Fatalf("no organization: %d", r.status)
	}
	body := advertiserBody()
	r := c.do(http.MethodPost, "/organizations", body)
	var org map[string]any
	r.json(t, &org)
	if r.status != http.StatusCreated || org["tax_document_uploaded"] != false || org["inn"] != body["inn"] {
		t.Fatalf("create: %d %s", r.status, r.body)
	}

	// Тот же ИНН у другого пользователя.
	other := a.client()
	other.login("other@example.ru")
	r = other.do(http.MethodPost, "/organizations", body)
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "inn_taken" {
		t.Fatalf("inn taken: %d %s", r.status, r.body)
	}
	// Поле блогера у рекламодателя.
	bad := advertiserBody()
	bad["tax_regime"] = "usn"
	r = other.do(http.MethodPost, "/organizations", bad)
	if code, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || code != "validation_failed" || details["field"] != "tax_regime" {
		t.Fatalf("blogger field: %d %s", r.status, r.body)
	}

	r = c.do(http.MethodPatch, "/organizations/current", map[string]string{"bank_bik": "044525000"})
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "044525000") {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r = c.do(http.MethodGet, "/organizations/current", nil); r.status != http.StatusOK {
		t.Fatalf("get: %d", r.status)
	}
	// Документ о режиме — только кабинету блогера.
	r = c.do(http.MethodPut, "/organizations/current/tax-document", fileBody("tax.pdf", []byte("%PDF-1.7 x")))
	if code, details := r.errorCode(t); r.status != http.StatusForbidden || code != "forbidden" || details["reason"] != "cabinet_required" {
		t.Fatalf("tax document for advertiser: %d %s", r.status, r.body)
	}

	r = c.do(http.MethodGet, "/legal-documents", nil)
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Version  int    `json:"version"`
			Title    string `json:"title"`
			URL      string `json:"url"`
			Accepted bool   `json:"accepted"`
		} `json:"items"`
	}
	r.json(t, &list)
	if len(list.Items) != 4 || list.Items[0].Version != 1 || list.Items[0].Title == "" || list.Items[0].URL == "" {
		t.Fatalf("legal documents: %s", r.body)
	}
	ids := []string{docs[domain.DocAdvertiserOffer], docs[domain.DocPlatformRules], docs[domain.DocPrivacyPolicy]}
	if r = c.do(http.MethodPost, "/legal-acceptances", map[string]any{"legal_document_ids": ids}); r.status != http.StatusNoContent {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	r = c.do(http.MethodPost, "/legal-acceptances", map[string]any{"legal_document_ids": []string{docs[domain.DocBloggerOffer]}})
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("accept blogger offer as advertiser: %d %s", r.status, r.body)
	}
	var row struct{ IP *netip.Addr }
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT ip FROM legal_acceptance LIMIT 1`).Scan(&row.IP); err != nil || row.IP == nil || !row.IP.IsLoopback() {
		t.Fatalf("acceptance ip: %v %v", row.IP, err)
	}
}

// Все операции спецификации реализованы (этап «Приём оплаты и учёт денег» — последние, тег
// balance); заглушка новых операций по-прежнему отвечает 501 not_implemented с именем операции.
func TestUnimplementedOperationWithSession(t *testing.T) {
	wr := httptest.NewRecorder()
	_, err := unimplemented{}.GetBalance(context.Background(), apigen.GetBalanceRequestObject{})
	httpx.WriteError(wr, httptest.NewRequest(http.MethodGet, "/api/v1/balance", nil), logx.Discard(), err)
	if wr.Code != http.StatusNotImplemented || !strings.Contains(wr.Body.String(), `"operation":"getBalance"`) ||
		!strings.Contains(wr.Body.String(), `"code":"not_implemented"`) {
		t.Fatalf("stub: %d %s", wr.Code, wr.Body)
	}
}
