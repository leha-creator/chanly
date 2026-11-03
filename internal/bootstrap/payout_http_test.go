package bootstrap

import (
	"net/http"
	"testing"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

type rocketworkView struct {
	Status          string  `json:"status"`
	Required        bool    `json:"required"`
	OnboardingURL   string  `json:"onboarding_url"`
	BlockedReason   string  `json:"blocked_reason"`
	StatusCheckedAt *string `json:"status_checked_at"`
}

type payoutView struct {
	ID          string `json:"id"`
	PlacementID string `json:"placement_id"`
	OrderNumber int64  `json:"order_number"`
	Channel     struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"channel"`
	Amount      int64  `json:"amount"`
	Status      string `json:"status"`
	Method      string `json:"method"`
	Attempt     int    `json:"attempt"`
	FailureCode string `json:"failure_code"`
	ReceiptURL  string `json:"receipt_url"`
}

type payoutPage struct {
	Items      []payoutView `json:"items"`
	NextCursor *string      `json:"next_cursor"`
	Total      *int         `json:"total"`
}

// legalBloggerClient — блогер-юрлицо: Рокет Ворк ему не нужен (A-36).
func (a *testApp) legalBloggerClient(email string) *client {
	a.t.Helper()
	c := a.client()
	c.login(email)
	body := advertiserBody()
	body["cabinet"] = "blogger"
	body["inn"] = accounttest.INN(accountdomain.LegalEntity)
	for k, v := range map[string]string{"kpp": "770701001", "tax_regime": "osno", "vat_rate": "vat_22",
		"signer_position": "Генеральный директор", "signer_name": "Тестов Иван Иванович", "signer_basis_name": "Устав",
		"signer_basis_number": "1"} {
		body[k] = v
	}
	if r := c.do(http.MethodPost, "/organizations", body); r.status != http.StatusCreated {
		a.t.Fatalf("create legal blogger: %d %s", r.status, r.body)
	}
	return c
}

// R-16 этапа выплат: операции blogger-payouts по спецификации.
func TestPayoutHTTP(t *testing.T) {
	a := newTestApp(t)
	blg := a.bloggerClient("blg-payouts@example.ru")

	var st rocketworkView
	blg.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Status != "active" || !st.Required || st.OnboardingURL == "" || st.StatusCheckedAt == nil {
		t.Fatalf("status %+v", st)
	}
	if r := blg.do(http.MethodPost, "/blogger/rocketwork/invite", nil); r.status != http.StatusAccepted {
		t.Fatalf("invite: %d %s", r.status, r.body)
	}
	if r := blg.do(http.MethodPost, "/blogger/rocketwork/refresh", nil); r.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", r.status, r.body)
	}

	// Выплаты: чужие не видны, курсор и итоги.
	org := blg.orgID(t)
	var ids []string
	for range 3 {
		_, _, id := a.acceptedPayoutFor(org, 10_000)
		ids = append(ids, id.String())
	}
	other := a.bloggerClient("blg-payouts-other@example.ru")
	a.acceptedPayoutFor(other.orgID(t), 20_000)

	var page payoutPage
	blg.do(http.MethodGet, "/blogger/payouts?limit=2", nil).json(t, &page)
	if len(page.Items) != 2 || page.NextCursor == nil || page.Total == nil || *page.Total != 3 {
		t.Fatalf("first page %+v", page)
	}
	if page.Items[0].Method != "rocketwork" || page.Items[0].Status != "created" || page.Items[0].Channel.Title == "" {
		t.Fatalf("item %+v", page.Items[0])
	}
	var rest payoutPage
	blg.do(http.MethodGet, "/blogger/payouts?limit=2&cursor="+*page.NextCursor, nil).json(t, &rest)
	if len(rest.Items) != 1 || rest.NextCursor != nil {
		t.Fatalf("second page %+v", rest)
	}
	seen := map[string]bool{}
	for _, p := range append(page.Items, rest.Items...) {
		seen[p.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("payout %s not listed", id)
		}
	}
	var sum struct {
		Received   int64 `json:"received_amount"`
		InProgress int64 `json:"in_progress_amount"`
		Count      int   `json:"count"`
	}
	blg.do(http.MethodGet, "/blogger/payouts/summary", nil).json(t, &sum)
	if sum.Received != 0 || sum.InProgress != 30_000 || sum.Count != 3 {
		t.Fatalf("summary %+v", sum)
	}
	if r := blg.do(http.MethodGet, "/blogger/payouts?cursor=broken", nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad cursor: %d", r.status)
	}

	// Юрлицо — 409 contractor_not_required, статус required=false.
	legal := a.legalBloggerClient("blg-payouts-legal@example.ru")
	legal.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Required || st.Status != "none" {
		t.Fatalf("legal status %+v", st)
	}
	r := legal.do(http.MethodPost, "/blogger/rocketwork/invite", nil)
	if code, details := r.errorCode(t); r.status != http.StatusConflict || code != "conflict" || details["reason"] != "contractor_not_required" {
		t.Fatalf("legal invite: %d %s", r.status, r.body)
	}

	// Рекламодателю операции блогера недоступны.
	adv := a.newAdvertiser("adv-payouts@example.ru")
	if r := adv.do(http.MethodGet, "/blogger/payouts", nil); r.status != http.StatusForbidden {
		t.Fatalf("advertiser: %d", r.status)
	}

	// Лимит частоты: 10 приглашений в час.
	var last response
	for range 11 {
		last = blg.do(http.MethodPost, "/blogger/rocketwork/invite", nil)
	}
	if last.status != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d %s", last.status, last.body)
	}
}

// R-1: ROCKETWORK_MODE=disabled — приглашение 503 rocketwork_unavailable.
func TestPayoutHTTPDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.RocketWork.Mode = RocketWorkDisabled
	a := newTestAppWith(t, cfg)
	c := a.client()
	c.login("blg-disabled@example.ru")
	r := c.do(http.MethodPost, "/organizations", map[string]any{
		"cabinet": "blogger", "legal_type": "self_employed", "inn": accounttest.INN(accountdomain.SelfEmployed),
		"name": "Анна Блогерова", "contact_name": "Анна Блогерова", "phone": "+79990001122", "tax_regime": "npd", "vat_rate": "none",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create blogger: %d %s", r.status, r.body)
	}
	r = c.do(http.MethodPost, "/blogger/rocketwork/invite", nil)
	if code, details := r.errorCode(t); r.status != http.StatusServiceUnavailable || code != "service_unavailable" ||
		details["reason"] != "rocketwork_unavailable" {
		t.Fatalf("disabled invite: %d %s", r.status, r.body)
	}
	if a.mods.payout.Config.ProviderEnabled {
		t.Fatal("provider enabled in disabled mode")
	}
}
