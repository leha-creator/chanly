package bootstrap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/domain"
)

type meView struct {
	Organizations []struct {
		ID       string   `json:"id"`
		Cabinets []string `json:"cabinets"`
	} `json:"organizations"`
	Pending []string `json:"pending_legal_document_ids"`
}

func (c *client) me(t *testing.T) meView {
	t.Helper()
	r := c.do(http.MethodGet, "/me", nil)
	if r.status != http.StatusOK {
		t.Fatalf("me: %d %s", r.status, r.body)
	}
	var m meView
	r.json(t, &m)
	return m
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			return false
		}
	}
	return true
}

// TestScenarios проходит S-01 и S-10 через HTTP и проверяет журнал (07 §5.3, §5.4).
func TestScenarios(t *testing.T) {
	a := newTestApp(t)
	docs := a.publishAll()
	var secrets []string // что не должно попасть в журнал

	// S-01: рекламодатель — ООО.
	adv := a.client()
	advEmail := "advertiser.s01@example.ru"
	secrets = append(secrets, advEmail)
	if me := adv.login(advEmail); len(me["organizations"].([]any)) != 0 {
		t.Fatalf("new user has organizations: %v", me)
	}
	secrets = append(secrets, a.mailCode(advEmail))
	advBody := advertiserBody()
	secrets = append(secrets, advBody["inn"].(string))
	if r := adv.do(http.MethodPost, "/organizations", advBody); r.status != http.StatusCreated {
		t.Fatalf("create advertiser: %d %s", r.status, r.body)
	}
	me := adv.me(t)
	want := []string{docs[domain.DocAdvertiserOffer], docs[domain.DocPlatformRules], docs[domain.DocPrivacyPolicy]}
	if !sameSet(me.Pending, want) {
		t.Fatalf("pending after S-01 step 3: %v", me.Pending)
	}
	if r := adv.do(http.MethodPost, "/legal-acceptances", map[string]any{"legal_document_ids": me.Pending}); r.status != http.StatusNoContent {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if me = adv.me(t); len(me.Pending) != 0 {
		t.Fatalf("pending after acceptance: %v", me.Pending)
	}
	secrets = append(secrets, adv.cookie(t, "session"), adv.cookie(t, "csrf_token"))

	// S-10: блогер — самозанятый на НПД.
	blg := a.client()
	blgEmail := "blogger.s10@example.ru"
	secrets = append(secrets, blgEmail)
	blg.login(blgEmail)
	in := accounttest.BloggerSelfEmployed()
	secrets = append(secrets, in.INN)
	r := blg.do(http.MethodPost, "/organizations", map[string]any{
		"cabinet": "blogger", "legal_type": "self_employed", "inn": in.INN, "name": *in.Fields.Name,
		"contact_name": *in.Fields.ContactName, "phone": *in.Fields.Phone, "tax_regime": "npd", "vat_rate": "none",
	})
	var org map[string]any
	r.json(t, &org)
	if r.status != http.StatusCreated || org["tax_regime"] != "npd" || org["blogger_full_name"] != *in.Fields.Name {
		t.Fatalf("create blogger: %d %s", r.status, r.body)
	}
	r = blg.do(http.MethodPut, "/organizations/current/tax-document", fileBody("npd.pdf", []byte("%PDF-1.7 npd certificate")))
	if r.json(t, &org); r.status != http.StatusOK || org["tax_document_uploaded"] != true {
		t.Fatalf("tax document: %d %s", r.status, r.body)
	}
	me = blg.me(t)
	if !sameSet(me.Pending, []string{docs[domain.DocBloggerOffer], docs[domain.DocPlatformRules], docs[domain.DocPrivacyPolicy]}) {
		t.Fatalf("blogger pending: %v", me.Pending)
	}
	blg.do(http.MethodPost, "/legal-acceptances", map[string]any{"legal_document_ids": me.Pending})

	// Тот же пользователь открывает кабинет рекламодателя: реквизиты прежние,
	// нужна только оферта рекламодателя.
	r = blg.do(http.MethodPost, "/organizations", map[string]any{
		"cabinet": "advertiser", "legal_type": "self_employed", "inn": in.INN, "name": "Другое имя",
		"contact_name": "Другой", "phone": "+79990009999",
	})
	if r.json(t, &org); r.status != http.StatusCreated || org["name"] != *in.Fields.Name || len(org["cabinets"].([]any)) != 2 {
		t.Fatalf("add advertiser cabinet: %d %s", r.status, r.body)
	}
	if me = blg.me(t); !sameSet(me.Pending, []string{docs[domain.DocAdvertiserOffer]}) {
		t.Fatalf("pending after second cabinet: %v", me.Pending)
	}

	// Отклонения.
	intruder := a.client()
	intruder.login("intruder@example.ru")
	secrets = append(secrets, "intruder@example.ru")
	r = intruder.do(http.MethodPost, "/organizations", advBody)
	if code, _ := r.errorCode(t); code != "inn_taken" {
		t.Fatalf("inn_taken: %d %s", r.status, r.body)
	}
	ip := accounttest.INN(domain.SoleProprietor)
	r = intruder.do(http.MethodPost, "/organizations", map[string]any{
		"cabinet": "blogger", "legal_type": "sole_proprietor", "inn": ip, "name": "ИП Сидоров", "contact_name": "Сидор",
		"phone": "+79990000003", "address": "Москва", "tax_regime": "osno", "vat_rate": "vat_5",
	})
	if code, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || code != "validation_failed" || details["field"] != "vat_rate" {
		t.Fatalf("osno + vat_5: %d %s", r.status, r.body)
	}
	intruder.csrf = false
	if r = intruder.do(http.MethodPatch, "/organizations/current", map[string]string{"phone": "+79990000004"}); r.status != http.StatusForbidden {
		t.Fatalf("without csrf: %d", r.status)
	}
	oldSession := adv.cookie(t, "session")
	if r = adv.do(http.MethodPost, "/auth/logout", nil); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d", r.status)
	}
	stale := a.client()
	u, _ := url.Parse(a.srv.URL)
	stale.http.Jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: oldSession, Path: "/"}})
	if r = stale.do(http.MethodGet, "/me", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("old session after logout: %d", r.status)
	}
	wrong := a.client()
	if r = wrong.do(http.MethodPost, "/auth/login", map[string]string{"email": advEmail, "code": "000001"}); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", r.status)
	}

	a.checkLog(t, secrets)
}

// mailCode — код из последнего письма на адрес.
func (a *testApp) mailCode(email string) string {
	m, _ := a.mail.Last(domain.NormalizeEmail(email))
	return m.Code
}

func (c *client) cookie(t *testing.T, name string) string {
	t.Helper()
	u, _ := url.Parse(c.app.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	t.Fatalf("no cookie %s", name)
	return ""
}

// checkLog проверяет, что обязательные события этапа записаны, а коды, токены,
// почта и ИНН в открытом виде — нет.
func (a *testApp) checkLog(t *testing.T, secrets []string) {
	t.Helper()
	logs := a.logs.Bytes()
	messages := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(logs))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if rec["time"] == nil || rec["level"] == nil || rec["component"] == nil {
			t.Fatalf("log line lacks required fields: %s", sc.Text())
		}
		messages[rec["msg"].(string)] = true
	}
	for _, m := range []string{
		"login code requested", "login code sent", "user logged in", "login failed", "user logged out",
		"organization created", "cabinet added", "organization rejected", "tax document uploaded",
		"legal documents accepted", "csrf check failed", "access denied", "job started", "job finished",
		"http request finished",
	} {
		if !messages[m] {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(string(logs), s) {
			t.Errorf("log contains a secret or personal data: %q", s)
		}
	}
}
