package bootstrap

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// Этап «API администратора»: деньги через HTTP — ручная проводка, пополнение депозита, возврат
// остатка, второе подтверждение (R-2), реестр выплат с CSV (R-14), журнал действий и счётчики.

type approvalView struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Kind        string `json:"kind"`
	ResultID    string `json:"result_id"`
	ObjectID    string `json:"object_id"`
	RequestedBy struct {
		Email string `json:"email"`
	} `json:"requested_by"`
}

func TestAdminApprovalsHTTP(t *testing.T) {
	a := newTestApp(t)
	s1 := a.adminClient("senior-a@example.ru", accountdomain.AdminSenior)
	s2 := a.adminClient("senior-b@example.ru", accountdomain.AdminSenior)
	mod := a.adminClient("mod-money@example.ru", accountdomain.AdminModerator)
	manual := func(amount int64) map[string]any {
		return map[string]any{"debit": map[string]any{"kind": "bank"}, "credit": map[string]any{"kind": "platform_revenue"},
			"amount": amount, "reason": "Комиссия банка"}
	}

	// moderator — 403 на операциях senior.
	for _, path := range []string{"/admin/approvals", "/admin/ledger/summary", "/admin/ledger/entries", "/admin/audit-log", "/admin/payouts"} {
		expectStatus(t, mod.do(http.MethodGet, path, nil), http.StatusForbidden, "moderator "+path)
	}
	expectStatus(t, mod.doKey(http.MethodPost, "/admin/ledger/manual", "m-0", manual(100)), http.StatusForbidden, "moderator manual")

	// Не больше порога — проводка сразу; повтор ключа — та же проводка.
	var small, again struct {
		TransactionID string `json:"transaction_id"`
		Replayed      bool   `json:"replayed"`
	}
	s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-1", manual(100_000)).json(t, &small)
	s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-1", manual(100_000)).json(t, &again)
	if small.TransactionID == "" || again.TransactionID != small.TransactionID || !again.Replayed {
		t.Fatalf("manual replay: %+v %+v", small, again)
	}
	r := s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-bad", map[string]any{"debit": map[string]any{"kind": "advertiser_balance"},
		"credit": map[string]any{"kind": "bank"}, "amount": 100, "reason": "x"})
	expectStatus(t, r, http.StatusUnprocessableEntity, "account without organization")

	// Больше порога — 202, проводки нет; свой запрос подтвердить нельзя.
	var req approvalView
	r = s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-2", manual(15_000_000))
	expectStatus(t, r, http.StatusAccepted, "manual above threshold")
	r.json(t, &req)
	if req.Status != "pending" || req.Kind != "ledger_manual" || req.ResultID != "" {
		t.Fatalf("approval: %+v", req)
	}
	if n := a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'manual' AND idempotency_key LIKE '%m-2'`); n != 0 {
		t.Fatalf("posted before approval: %d", n)
	}
	var repeat approvalView
	s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-2", manual(15_000_000)).json(t, &repeat)
	if repeat.ID != req.ID {
		t.Fatalf("repeat key created another approval: %s %s", repeat.ID, req.ID)
	}
	// Тот же ключ с другой суммой — отказ, а не старый запрос под видом нового.
	r = s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-2", manual(16_000_000))
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "idempotency_conflict" {
		t.Fatalf("key reuse: %d %s", r.status, r.body)
	}
	var pending page[approvalView]
	s2.do(http.MethodGet, "/admin/approvals", nil).json(t, &pending)
	if len(pending.Items) != 1 || pending.Items[0].ID != req.ID || *pending.Total != 1 {
		t.Fatalf("pending: %+v", pending)
	}
	r = s1.do(http.MethodPost, "/admin/approvals/"+req.ID+"/approve", nil)
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "same_admin" {
		t.Fatalf("same admin: %d %s", r.status, r.body)
	}
	var approved approvalView
	r = s2.do(http.MethodPost, "/admin/approvals/"+req.ID+"/approve", nil)
	expectStatus(t, r, http.StatusOK, "approve")
	r.json(t, &approved)
	if approved.Status != "approved" || approved.ResultID == "" {
		t.Fatalf("approved: %+v", approved)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'ledger.manual' AND object_id = $1 AND data ->> 'approver_id' IS NOT NULL`,
		approved.ResultID); n != 1 {
		t.Fatalf("ledger audit with approver: %d", n)
	}
	r = s2.do(http.MethodPost, "/admin/approvals/"+req.ID+"/approve", nil)
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "approval_decided" {
		t.Fatalf("decided: %d %s", r.status, r.body)
	}
	var entries page[struct {
		ID             string `json:"id"`
		ApproverUserID string `json:"approver_user_id"`
	}]
	s1.do(http.MethodGet, "/admin/ledger/entries?kind=manual", nil).json(t, &entries)
	found := false
	for _, e := range entries.Items {
		found = found || (e.ID == approved.ResultID && e.ApproverUserID != "")
	}
	if !found || *entries.Total < 2 {
		t.Fatalf("entries: %+v", entries)
	}

	// Пополнение депозита выше порога: автор отзывает запрос.
	var topup approvalView
	r = s1.doKey(http.MethodPost, "/admin/ledger/deposit-topups", "t-1", map[string]any{"amount": 20_000_000, "reason": "платёжка 77"})
	expectStatus(t, r, http.StatusAccepted, "topup above threshold")
	r.json(t, &topup)
	var rejected approvalView
	s1.do(http.MethodPost, "/admin/approvals/"+topup.ID+"/reject", map[string]any{"reason": "ошибка в сумме"}).json(t, &rejected)
	if rejected.Status != "rejected" {
		t.Fatalf("withdraw: %+v", rejected)
	}
	expectStatus(t, s1.doKey(http.MethodPost, "/admin/ledger/deposit-topups", "t-2", map[string]any{"amount": 500_000, "reason": "платёжка 78"}),
		http.StatusCreated, "small topup")

	// Возврат больше свободного остатка — 409 insufficient_balance.
	a.newAdvertiser("adv-return2@example.ru")
	orgID := a.orgOf("adv-return2@example.ru")
	r = s1.doKey(http.MethodPost, "/admin/organizations/"+orgID+"/balance-returns", "r-1", map[string]any{"amount": 100, "reason": "заявление"})
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "insufficient_balance" {
		t.Fatalf("balance return: %d %s", r.status, r.body)
	}

	// Сверка и журнал действий.
	var summary struct {
		Balanced bool `json:"balanced"`
		Accounts []struct {
			Kind string `json:"kind"`
		} `json:"accounts"`
	}
	s1.do(http.MethodGet, "/admin/ledger/summary", nil).json(t, &summary)
	if !summary.Balanced || len(summary.Accounts) != 7 {
		t.Fatalf("summary: %+v", summary)
	}
	var audit page[struct {
		Action string         `json:"action"`
		Data   map[string]any `json:"data"`
	}]
	s1.do(http.MethodGet, "/admin/audit-log?manual_only=true&action=admin_approval.approved", nil).json(t, &audit)
	if len(audit.Items) != 1 || audit.Items[0].Action != "admin_approval.approved" {
		t.Fatalf("audit: %+v", audit)
	}
	if bytes.Contains(s1.do(http.MethodGet, "/admin/audit-log", nil).body, []byte(`"ip"`)) {
		t.Fatal("audit log exposes ip")
	}

	// Срок запроса: после ADMIN_APPROVAL_TTL — expired, подтвердить нельзя.
	var late approvalView
	s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-3", manual(12_000_000)).json(t, &late)
	var late2 approvalView
	s1.doKey(http.MethodPost, "/admin/ledger/manual", "m-4", manual(13_000_000)).json(t, &late2)
	a.clk.Advance(73 * time.Hour)
	s3 := a.adminClient("senior-c@example.ru", accountdomain.AdminSenior)
	// Срок проверяется при подтверждении, а не только при чтении списка.
	r = s3.do(http.MethodPost, "/admin/approvals/"+late2.ID+"/approve", nil)
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "approval_expired" {
		t.Fatalf("expired approve without list: %d %s", r.status, r.body)
	}
	var expired page[approvalView]
	s3.do(http.MethodGet, "/admin/approvals?status=expired", nil).json(t, &expired)
	if len(expired.Items) != 2 {
		t.Fatalf("expired: %+v", expired)
	}
	r = s3.do(http.MethodPost, "/admin/approvals/"+late.ID+"/approve", nil)
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "approval_expired" {
		t.Fatalf("expired approve: %d %s", r.status, r.body)
	}
	var counters struct {
		ApprovalsPending int `json:"approvals_pending"`
	}
	mod2 := a.adminClient("mod-money2@example.ru", accountdomain.AdminModerator)
	expectStatus(t, mod2.do(http.MethodGet, "/admin/counters", nil), http.StatusOK, "counters")
	mod2.do(http.MethodGet, "/admin/counters", nil).json(t, &counters)
	if counters.ApprovalsPending != 0 {
		t.Fatalf("counters: %+v", counters)
	}
	if n := a.count(`SELECT count(*) FROM admin_approval WHERE status = 'expired'`); n != 2 {
		t.Fatalf("expired rows %d", n)
	}
}

func TestAdminPayoutsHTTP(t *testing.T) {
	a := newTestApp(t)
	s1 := a.adminClient("senior-p1@example.ru", accountdomain.AdminSenior)
	s2 := a.adminClient("senior-p2@example.ru", accountdomain.AdminSenior)
	_, legal, small := a.acceptedPayout(true, 80_000)
	_, _, big := a.acceptedPayout(true, 12_000_000)

	var list page[struct {
		ID      string `json:"id"`
		Method  string `json:"method"`
		Blogger struct {
			Inn string `json:"inn"`
		} `json:"blogger"`
	}]
	s1.do(http.MethodGet, "/admin/payouts?status=created&organization_id="+legal.String(), nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != small.String() || list.Items[0].Method != "bank_transfer" ||
		!strings.Contains(list.Items[0].Blogger.Inn, "*") || *list.Total != 1 {
		t.Fatalf("list: %+v", list)
	}

	// До порога — сразу succeeded; выше — 202 и подтверждение другим senior.
	var paid struct {
		Status        string `json:"status"`
		BankReference string `json:"bank_reference"`
	}
	s1.doKey(http.MethodPost, "/admin/payouts/"+small.String()+"/mark-paid", "p-1",
		map[string]any{"bank_reference": "п/п 1", "reason": "оплачено"}).json(t, &paid)
	if paid.Status != "succeeded" || paid.BankReference != "п/п 1" {
		t.Fatalf("mark paid: %+v", paid)
	}
	var req approvalView
	r := s1.doKey(http.MethodPost, "/admin/payouts/"+big.String()+"/mark-paid", "p-2", map[string]any{"bank_reference": "п/п 2", "reason": "оплачено"})
	expectStatus(t, r, http.StatusAccepted, "mark paid above threshold")
	r.json(t, &req)
	if req.Kind != "payout_mark_paid" || req.ObjectID != big.String() {
		t.Fatalf("approval: %+v", req)
	}
	var approved approvalView
	s2.do(http.MethodPost, "/admin/approvals/"+req.ID+"/approve", nil).json(t, &approved)
	if approved.Status != "approved" || approved.ResultID != big.String() {
		t.Fatalf("approved: %+v", approved)
	}
	if n := a.count(`SELECT count(*) FROM payout WHERE id = $1 AND status = 'succeeded'`, big); n != 1 {
		t.Fatal("payout not paid after approval")
	}
	expectStatus(t, s1.do(http.MethodPost, "/admin/payouts/"+small.String()+"/retry", map[string]any{"reason": "ещё"}),
		http.StatusConflict, "retry of succeeded payout")

	var summary struct {
		ByStatus []struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		} `json:"by_status"`
	}
	s1.do(http.MethodGet, "/admin/payouts/summary", nil).json(t, &summary)
	if len(summary.ByStatus) == 0 {
		t.Fatalf("summary: %+v", summary)
	}

	// CSV: BOM, разделитель «;», ИНН с маской; выгрузка в журнале действий.
	csv := s1.do(http.MethodGet, "/admin/payouts/export?status=succeeded", nil)
	expectStatus(t, csv, http.StatusOK, "export")
	body, text := csv.body, string(csv.body)
	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) || !strings.Contains(text, "Создана (МСК);Блогер;ИНН") ||
		strings.Count(text, "\n") != 3 || !strings.Contains(text, "800,00") {
		t.Fatalf("csv: %q", text)
	}
	var inn string
	_ = a.db.Pool.QueryRow(t.Context(), `SELECT inn FROM organization WHERE id = $1`, legal).Scan(&inn)
	if inn == "" || strings.Contains(text, inn) {
		t.Fatalf("csv exposes inn %q", inn)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'payout.exported'`); n != 1 {
		t.Fatalf("export audit %d", n)
	}
}

// orgOf — организация пользователя email.
func (a *testApp) orgOf(email string) string {
	a.t.Helper()
	var id string
	if err := a.db.Pool.QueryRow(a.t.Context(), `SELECT m.organization_id::text FROM organization_member m
		JOIN "user" u ON u.id = m.user_id WHERE u.email = $1`, email).Scan(&id); err != nil {
		a.t.Fatalf("org of %s: %v", email, err)
	}
	return id
}

// S-30 через HTTP: загрузка выписки, сопоставление и отказ, счета всех организаций.
func TestAdminPaymentsHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("adv-pay@example.ru")
	org := adv.orgID(t)
	s1 := a.adminClient("senior-pay@example.ru", accountdomain.AdminSenior)
	mod := a.adminClient("mod-pay@example.ru", accountdomain.AdminModerator)
	var inv, other invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 2_000_000}).json(t, &inv)
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 3_000_000}).json(t, &other)

	expectStatus(t, s1.do(http.MethodGet, "/admin/bank-statements/latest", nil), http.StatusNotFound, "no statements")
	path := testStatement(t, 2_150_000, [3]string{"20000.00", "7707083893", "Оплата по счёту " + inv.Number},
		[3]string{"1500.00", "7707083893", "Оплата от ИНН 7707083893"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, mod.do(http.MethodPost, "/admin/bank-statements", fileBody("statement.txt", data)), http.StatusForbidden, "moderator upload")
	var sum struct {
		ID             string `json:"id"`
		MatchedCount   int    `json:"matched_count"`
		UnmatchedCount int    `json:"unmatched_count"`
		ClosingBalance int64  `json:"closing_balance"`
		UploadedBy     struct {
			Email string `json:"email"`
		} `json:"uploaded_by"`
	}
	r := s1.do(http.MethodPost, "/admin/bank-statements", fileBody("statement.txt", data))
	expectStatus(t, r, http.StatusCreated, "upload")
	r.json(t, &sum)
	if sum.MatchedCount != 1 || sum.UnmatchedCount != 1 || sum.ClosingBalance != 2_150_000 || sum.UploadedBy.Email != "senior-pay@example.ru" {
		t.Fatalf("summary: %+v", sum)
	}
	r = s1.do(http.MethodPost, "/admin/bank-statements", fileBody("statement.txt", data))
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "statement_already_imported" {
		t.Fatalf("repeat: %d %s", r.status, r.body)
	}
	var latest struct {
		ID string `json:"id"`
	}
	s1.do(http.MethodGet, "/admin/bank-statements/latest", nil).json(t, &latest)
	if latest.ID != sum.ID {
		t.Fatalf("latest %s", latest.ID)
	}

	var unmatched page[struct {
		ID       string `json:"id"`
		PayerInn string `json:"payer_inn"`
	}]
	s1.do(http.MethodGet, "/admin/bank-transactions?match_status=unmatched", nil).json(t, &unmatched)
	if len(unmatched.Items) != 1 || unmatched.Items[0].PayerInn != "7707083893" || *unmatched.Total != 1 {
		t.Fatalf("unmatched: %+v", unmatched)
	}
	bt := unmatched.Items[0].ID
	var matched struct {
		MatchStatus     string `json:"match_status"`
		MatchedManually bool   `json:"matched_manually"`
		InvoiceNumber   string `json:"invoice_number"`
		Organization    struct {
			Inn string `json:"inn"`
		} `json:"organization"`
	}
	r = s1.do(http.MethodPost, "/admin/bank-transactions/"+bt+"/match", map[string]any{"organization_id": org, "invoice_id": other.ID,
		"reason": "плательщик — директор"})
	expectStatus(t, r, http.StatusOK, "match")
	r.json(t, &matched)
	if matched.MatchStatus != "matched" || !matched.MatchedManually || matched.InvoiceNumber != other.Number ||
		!strings.Contains(matched.Organization.Inn, "*") {
		t.Fatalf("matched: %+v", matched)
	}
	expectStatus(t, s1.do(http.MethodPost, "/admin/bank-transactions/"+bt+"/ignore", map[string]any{"reason": "посторонний"}),
		http.StatusConflict, "ignore matched")
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'bank_transaction.matched' AND object_id = $1`, bt); n != 1 {
		t.Fatalf("match audit %d", n)
	}

	var third invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 1_000_000}).json(t, &third)
	var invoices page[struct {
		Number string `json:"number"`
		Status string `json:"status"`
	}]
	s1.do(http.MethodGet, "/admin/invoices?organization_id="+org.String(), nil).json(t, &invoices)
	if *invoices.Total != 3 {
		t.Fatalf("invoices: %+v", invoices)
	}
	var cancelled struct {
		Status       string `json:"status"`
		CancelReason string `json:"cancel_reason"`
	}
	s1.do(http.MethodPost, "/admin/invoices/"+third.ID+"/cancel", map[string]any{"reason": "перевыставить"}).json(t, &cancelled)
	if cancelled.Status != "cancelled" || cancelled.CancelReason != "перевыставить" {
		t.Fatalf("cancel: %+v", cancelled)
	}
	expectStatus(t, s1.do(http.MethodPost, "/admin/invoices/"+inv.ID+"/cancel", map[string]any{"reason": "поздно"}),
		http.StatusConflict, "cancel paid invoice")
}
