package handlers

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// Деньги администратора: реестр выплат, выписки и счета (S-30), журнал проводок и второе
// подтверждение (R-2). Модель денег мока упрощена: журнал проводок ведётся отдельно от балансов.

// ---------------------------------------------------------------- выплаты

func payoutsFiltered(tx *store.Tx, status *api.PayoutStatus, orgID *uuid.UUID, from, to *openapi_types.Date) []*store.Payout {
	var out []*store.Payout
	for _, po := range tx.D.Payouts {
		switch {
		case status != nil && string(*status) != po.Status,
			orgID != nil && *orgID != po.OrgID,
			!inPeriod(po.CreatedAt, from, to):
			continue
		}
		out = append(out, po)
	}
	return newestFirst(out, func(po *store.Payout) time.Time { return po.CreatedAt })
}

func (h *Handlers) ListAdminPayouts(ctx context.Context, req api.ListAdminPayoutsRequestObject) (api.ListAdminPayoutsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	pg, next, total := page(payoutsFiltered(tx, q.Status, q.OrganizationId, q.From, q.To), q.Limit, q.Cursor)
	items := []api.AdminPayout{}
	for _, po := range pg {
		items = append(items, adminPayout(tx, po))
	}
	return api.ListAdminPayouts200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetAdminPayoutSummary(ctx context.Context, req api.GetAdminPayoutSummaryRequestObject) (api.GetAdminPayoutSummaryResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	out := api.GetAdminPayoutSummary200JSONResponse{ByStatus: []api.AdminPayoutStatusTotal{}}
	totals := map[string]*api.AdminPayoutStatusTotal{}
	for _, s := range []string{"created", "sent", "succeeded", "failed"} {
		totals[s] = &api.AdminPayoutStatusTotal{Status: api.PayoutStatus(s)}
	}
	for _, po := range tx.D.Payouts {
		t := totals[po.Status]
		t.Count++
		t.Amount += po.Amount
		if po.Status == "created" || po.Status == "sent" {
			out.InProgressAmount += po.Amount
		}
	}
	for _, s := range []string{"created", "sent", "succeeded", "failed"} {
		out.ByStatus = append(out.ByStatus, *totals[s])
	}
	bal := tx.LedgerBalances()
	out.DepositLedgerAmount, out.PayableAmount = bal["rocketwork_deposit"], max(bal["blogger_payable"], 0)
	out.ShortageAmount = max(out.PayableAmount-out.DepositLedgerAmount, 0)
	return out, nil
}

func rubles(k int64) string { return fmt.Sprintf("%d,%02d", k/100, k%100) }

func (h *Handlers) ExportAdminPayouts(ctx context.Context, req api.ExportAdminPayoutsRequestObject) (api.ExportAdminPayoutsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	q := req.Params
	var b bytes.Buffer
	b.WriteString("\uFEFF")
	w := csv.NewWriter(&b)
	w.Comma = ';'
	_ = w.Write([]string{"Создана", "Заказ", "Канал", "Получатель", "ИНН", "Способ", "Попытка", "Сумма, ₽", "Состояние", "Причина", "Выполнена"})
	rows := payoutsFiltered(tx, q.Status, q.OrganizationId, q.From, q.To)
	for _, po := range rows {
		a := adminPayout(tx, po)
		completed, failure := "", ""
		if a.CompletedAt != nil {
			completed = a.CompletedAt.In(store.MSK).Format("02.01.2006 15:04")
		}
		if a.FailureCode != nil {
			failure = string(*a.FailureCode)
		}
		_ = w.Write([]string{a.CreatedAt.In(store.MSK).Format("02.01.2006 15:04"), itoa(a.OrderNumber), a.Channel.Title,
			a.Blogger.Name, a.Blogger.Inn, string(a.Method), fmt.Sprint(a.Attempt), rubles(a.Amount), string(a.Status), failure, completed})
	}
	w.Flush()
	tx.Audit(u.ID, nil, "payout.exported", "payout", "registry", "", map[string]any{"count": len(rows)})
	h.logger(ctx).Info("payouts exported", "op", "Admin.ExportPayouts", "count", len(rows), "admin_user_id", u.ID)
	return api.ExportAdminPayouts200TextcsvResponse{Body: bytes.NewReader(b.Bytes()), ContentLength: int64(b.Len())}, nil
}

func (h *Handlers) RetryPayout(ctx context.Context, req api.RetryPayoutRequestObject) (api.RetryPayoutResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	po, err := tx.PayoutByID(req.Id)
	if err != nil {
		return nil, err
	}
	n, err := tx.RetryPayout(u.ID, po, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	h.logger(ctx).Warn("admin payout retry", "op", "Admin.RetryPayout", "payout_id", n.ID, "admin_user_id", u.ID)
	return api.RetryPayout200JSONResponse(adminPayout(tx, n)), nil
}

func (h *Handlers) MarkPayoutPaid(ctx context.Context, req api.MarkPayoutPaidRequestObject) (api.MarkPayoutPaidResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	po, err := tx.PayoutByID(req.Id)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if po.Amount > store.SecondApprovalThreshold {
		if po.Method != "bank_transfer" {
			return nil, store.ConflictReason("payout_not_bank_transfer", "payout is not a bank transfer", nil)
		}
		id := po.ID
		a := tx.RequestApproval(&store.Approval{Kind: "payout_mark_paid", Amount: po.Amount, ObjectID: &id,
			Summary: "Выплата " + rubles(po.Amount) + " ₽ отмечена выполненной: " + b.BankReference, Reason: b.Reason,
			Key: req.Params.IdempotencyKey, RequestedBy: u.ID, BankReference: b.BankReference})
		return api.MarkPayoutPaid202JSONResponse{ApprovalRequiredJSONResponse: api.ApprovalRequiredJSONResponse(approval(tx, a))}, nil
	}
	if err := tx.MarkPayoutPaid(u.ID, nil, po, b.BankReference, b.Reason); err != nil {
		return nil, err
	}
	return api.MarkPayoutPaid200JSONResponse(adminPayout(tx, po)), nil
}

// ---------------------------------------------------------------- выписки, поступления, счета

func statementSummary(tx *store.Tx, s *store.BankStatement) api.BankStatementSummary {
	return api.BankStatementSummary{Id: s.ID, AccountNumber: s.AccountNumber, DateFrom: ptr(dateOf(s.DateFrom)),
		DateTo: ptr(dateOf(s.DateTo)), OpeningBalance: ptr(s.OpeningBalance), ClosingBalance: ptr(s.ClosingBalance),
		IncomingCount: s.Incoming, MatchedCount: s.Matched, UnmatchedCount: s.Unmatched, DuplicateCount: s.Duplicate,
		UploadedBy: userRef(tx, s.UploadedBy), CreatedAt: s.CreatedAt}
}

func (h *Handlers) UploadBankStatement(ctx context.Context, req api.UploadBankStatementRequestObject) (api.UploadBankStatementResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	size := 0
	for {
		part, err := req.Body.NextPart()
		if err != nil {
			break
		}
		n, _ := io.Copy(io.Discard, part)
		size += int(n)
	}
	if size == 0 {
		return nil, store.Validation("file", "statement file is empty")
	}
	// Разбор «1С» мок не делает: выписка с одним несопоставленным поступлением.
	day := tx.Now.In(store.MSK)
	s := &store.BankStatement{ID: store.NewID(), UploadedBy: u.ID, AccountNumber: "40702810000000009999",
		DateFrom: day, DateTo: day, OpeningBalance: 120_000_000, ClosingBalance: 125_000_000, Incoming: 1, Unmatched: 1, CreatedAt: tx.Now}
	tx.D.BankStatements = append(tx.D.BankStatements, s)
	tx.D.BankTxs = append(tx.D.BankTxs, &store.BankTx{ID: store.NewID(), StatementID: s.ID, OperationDate: day, Amount: 5_000_000,
		PayerName: "ООО «Неизвестный плательщик»", PayerINN: "7700000099", PayerAccount: "40702810000000000099",
		Purpose: "Оплата рекламы без номера счёта", MatchStatus: "unmatched", CreatedAt: tx.Now})
	tx.Audit(u.ID, nil, "bank_statement.imported", "bank_statement", s.ID.String(), "", map[string]any{"incoming": s.Incoming})
	return api.UploadBankStatement201JSONResponse(statementSummary(tx, s)), nil
}

func (h *Handlers) GetLatestBankStatement(ctx context.Context, req api.GetLatestBankStatementRequestObject) (api.GetLatestBankStatementResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	if len(tx.D.BankStatements) == 0 {
		return nil, store.NotFound("bank_statement", "latest")
	}
	return api.GetLatestBankStatement200JSONResponse(statementSummary(tx, tx.D.BankStatements[len(tx.D.BankStatements)-1])), nil
}

func bankTx(tx *store.Tx, t *store.BankTx) api.BankTransaction {
	out := api.BankTransaction{Id: t.ID, OperationDate: dateOf(t.OperationDate), Amount: t.Amount, PayerName: strPtr(t.PayerName),
		PayerInn: strPtr(t.PayerINN), PayerAccount: strPtr(t.PayerAccount), Purpose: strPtr(t.Purpose),
		MatchStatus: api.BankMatchStatus(t.MatchStatus), MatchedManually: ptr(t.MatchedBy != nil),
		IgnoredReason: strPtr(t.IgnoredReason), CreatedAt: t.CreatedAt}
	if t.OrgID != nil {
		out.Organization = ptr(orgRef(tx, *t.OrgID))
	}
	if t.InvoiceID != nil {
		for _, i := range tx.D.Invoices {
			if i.ID == *t.InvoiceID {
				out.InvoiceNumber = ptr(i.Number)
			}
		}
	}
	return out
}

func (h *Handlers) ListBankTransactions(ctx context.Context, req api.ListBankTransactionsRequestObject) (api.ListBankTransactionsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.BankTx
	for _, t := range tx.D.BankTxs {
		if q.MatchStatus == nil || string(*q.MatchStatus) == t.MatchStatus {
			found = append(found, t)
		}
	}
	found = newestFirst(found, func(t *store.BankTx) time.Time { return t.CreatedAt })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.BankTransaction{}
	for _, t := range pg {
		items = append(items, bankTx(tx, t))
	}
	return api.ListBankTransactions200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func findBankTx(tx *store.Tx, id uuid.UUID) (*store.BankTx, error) {
	for _, t := range tx.D.BankTxs {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, store.NotFound("bank_transaction", id)
}

func (h *Handlers) MatchBankTransaction(ctx context.Context, req api.MatchBankTransactionRequestObject) (api.MatchBankTransactionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	t, err := findBankTx(tx, req.Id)
	if err != nil {
		return nil, err
	}
	if t.MatchStatus == "matched" {
		return nil, store.ConflictReason("bank_transaction_matched", "bank transaction is already matched", nil)
	}
	b := req.Body
	o, err := tx.Org(b.OrganizationId)
	if err != nil {
		return nil, store.Validation("organization_id", "organization not found")
	}
	if b.InvoiceId != nil {
		inv, err := tx.Invoice(o.ID, *b.InvoiceId)
		if err != nil {
			return nil, store.Validation("invoice_id", "invoice not found for organization")
		}
		now, amount := tx.Now, t.Amount
		inv.Status, inv.PaidAt, inv.PaidAmount = "paid", &now, &amount
		t.InvoiceID = &inv.ID
	}
	id, by := o.ID, u.ID
	t.MatchStatus, t.OrgID, t.MatchedBy, t.IgnoredReason = "matched", &id, &by, ""
	tx.D.Ledger = append(tx.D.Ledger, &store.LedgerEntry{ID: store.NewID(), OrgID: o.ID, Kind: "deposit", Amount: t.Amount,
		Description: "Зачисление поступления по выписке", InvoiceID: t.InvoiceID, CreatedAt: tx.Now})
	tx.D.AdminLedger = append(tx.D.AdminLedger, &store.AdminLedgerTx{ID: store.NewID(), Kind: "deposit", Amount: t.Amount,
		Debit: store.LedgerAccount{Kind: "bank"}, Credit: store.LedgerAccount{Kind: "advertiser_balance", OrgID: &id},
		ActorType: "admin", ActorID: &by, Reason: b.Reason, CreatedAt: tx.Now})
	tx.Audit(u.ID, &id, "bank_transaction.matched", "bank_transaction", t.ID.String(), b.Reason, map[string]any{"amount": t.Amount})
	return api.MatchBankTransaction200JSONResponse(bankTx(tx, t)), nil
}

func (h *Handlers) IgnoreBankTransaction(ctx context.Context, req api.IgnoreBankTransactionRequestObject) (api.IgnoreBankTransactionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	t, err := findBankTx(tx, req.Id)
	if err != nil {
		return nil, err
	}
	if t.MatchStatus != "unmatched" {
		return nil, store.ConflictReason("bank_transaction_not_unmatched", "only unmatched transaction can be ignored", nil)
	}
	t.MatchStatus, t.IgnoredReason = "ignored", req.Body.Reason
	tx.Audit(u.ID, nil, "bank_transaction.ignored", "bank_transaction", t.ID.String(), req.Body.Reason, nil)
	return api.IgnoreBankTransaction200JSONResponse(bankTx(tx, t)), nil
}

func adminInvoice(tx *store.Tx, i *store.Invoice) api.AdminInvoice {
	out := recast[api.AdminInvoice](invoice(i))
	out.Organization = orgRef(tx, i.OrgID)
	out.CancelReason = strPtr(i.CancelReason)
	return out
}

func (h *Handlers) ListAdminInvoices(ctx context.Context, req api.ListAdminInvoicesRequestObject) (api.ListAdminInvoicesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.Invoice
	for _, i := range tx.D.Invoices {
		switch {
		case q.Status != nil && string(*q.Status) != i.Status,
			q.OrganizationId != nil && *q.OrganizationId != i.OrgID,
			q.Number != nil && *q.Number != i.Number:
			continue
		}
		found = append(found, i)
	}
	found = newestFirst(found, func(i *store.Invoice) time.Time { return i.CreatedAt })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.AdminInvoice{}
	for _, i := range pg {
		items = append(items, adminInvoice(tx, i))
	}
	return api.ListAdminInvoices200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) CancelAdminInvoice(ctx context.Context, req api.CancelAdminInvoiceRequestObject) (api.CancelAdminInvoiceResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	var inv *store.Invoice
	for _, i := range tx.D.Invoices {
		if i.ID == req.Id {
			inv = i
		}
	}
	if inv == nil {
		return nil, store.NotFound("invoice", req.Id)
	}
	if inv.Status != "issued" {
		return nil, store.ConflictReason("invoice_not_issued", "only issued invoice can be cancelled", map[string]any{"status": inv.Status})
	}
	inv.Status, inv.CancelReason = "cancelled", req.Body.Reason
	tx.Audit(u.ID, &inv.OrgID, "invoice.cancelled", "invoice", inv.ID.String(), req.Body.Reason, nil)
	return api.CancelAdminInvoice200JSONResponse(adminInvoice(tx, inv)), nil
}

// ---------------------------------------------------------------- журнал денег

var ledgerKinds = []string{"bank", "rocketwork_deposit", "payout_fees", "diagnostic_expense", "advertiser_balance", "placement_hold", "blogger_payable", "platform_revenue"}

func (h *Handlers) GetLedgerSummary(ctx context.Context, req api.GetLedgerSummaryRequestObject) (api.GetLedgerSummaryResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	bal := tx.LedgerBalances()
	out := api.GetLedgerSummary200JSONResponse{Accounts: []api.LedgerAccountTotal{}}
	var assets, liabilities int64
	for _, k := range ledgerKinds {
		out.Accounts = append(out.Accounts, api.LedgerAccountTotal{Kind: api.LedgerAccountKind(k), Balance: bal[k]})
		if slices.Contains([]string{"bank", "rocketwork_deposit", "payout_fees", "diagnostic_expense"}, k) {
			assets += bal[k]
		} else {
			liabilities += bal[k]
		}
	}
	out.Balanced = assets == liabilities
	out.AdvertiserLiabilities = bal["advertiser_balance"] + bal["placement_hold"]
	out.BankLedgerAmount = bal["bank"]
	if n := len(tx.D.BankStatements); n > 0 {
		s := tx.D.BankStatements[n-1]
		out.BankStatementAmount, out.BankStatementDate = ptr(s.ClosingBalance), ptr(dateOf(s.DateTo))
		out.BankDifference = ptr(s.ClosingBalance - bal["bank"])
	}
	return out, nil
}

func ledgerAccount(a store.LedgerAccount) api.LedgerAccountRef {
	return api.LedgerAccountRef{Kind: api.LedgerAccountKind(a.Kind), OrganizationId: a.OrgID, PlacementId: a.PlacementID}
}

func (h *Handlers) ListLedgerEntries(ctx context.Context, req api.ListLedgerEntriesRequestObject) (api.ListLedgerEntriesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.AdminLedgerTx
	for _, t := range tx.D.AdminLedger {
		org := (t.Debit.OrgID != nil && q.OrganizationId != nil && *t.Debit.OrgID == *q.OrganizationId) ||
			(t.Credit.OrgID != nil && q.OrganizationId != nil && *t.Credit.OrgID == *q.OrganizationId)
		switch {
		case q.Kind != nil && string(*q.Kind) != t.Kind,
			q.OrganizationId != nil && !org,
			!inPeriod(t.CreatedAt, q.From, q.To):
			continue
		}
		found = append(found, t)
	}
	found = newestFirst(found, func(t *store.AdminLedgerTx) time.Time { return t.CreatedAt })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.LedgerEntry{}
	for _, t := range pg {
		items = append(items, api.LedgerEntry{Id: t.ID, Kind: api.LedgerTransactionKind(t.Kind), Amount: t.Amount,
			Debit: ledgerAccount(t.Debit), Credit: ledgerAccount(t.Credit), ActorType: api.LedgerEntryActorType(t.ActorType),
			ActorUserId: t.ActorID, ApproverUserId: t.ApproverID, Reason: strPtr(t.Reason), PlacementId: t.PlacementID,
			InvoiceId: t.InvoiceID, PayoutId: t.PayoutID, CreatedAt: t.CreatedAt})
	}
	return api.ListLedgerEntries200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func storeAccount(a api.LedgerAccountRef) (store.LedgerAccount, error) {
	out := store.LedgerAccount{Kind: string(a.Kind), OrgID: a.OrganizationId, PlacementID: a.PlacementId}
	needOrg := out.Kind == "advertiser_balance" || out.Kind == "blogger_payable"
	if needOrg != (out.OrgID != nil) || (out.Kind == "placement_hold") != (out.PlacementID != nil) {
		return out, store.Invalid("debit", "account_scope", "account scope does not match its kind")
	}
	return out, nil
}

// ledgerOp — ручная проводка или запрос подтверждения выше порога.
func (h *Handlers) ledgerOp(ctx context.Context, kind, key string, debit, credit store.LedgerAccount, amount int64, reason string) (*api.LedgerOperationResult, *api.AdminApproval, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if a := tx.ApprovalByKey(key); a != nil {
		out := approval(tx, a)
		return nil, &out, nil
	}
	if amount > store.SecondApprovalThreshold && kind != "balance_return" {
		akind := "ledger_manual"
		if kind == "deposit_topup" {
			akind = "deposit_topup"
		}
		a := tx.RequestApproval(&store.Approval{Kind: akind, Amount: amount, Debit: debit, Credit: credit,
			Summary: debit.Kind + " → " + credit.Kind + ", " + rubles(amount) + " ₽", Reason: reason, Key: key, RequestedBy: u.ID})
		out := approval(tx, a)
		return nil, &out, nil
	}
	if kind == "balance_return" {
		available, _ := tx.Balance(*debit.OrgID)
		if amount > available {
			return nil, nil, store.Conflict("insufficient_balance", "amount exceeds free balance", map[string]any{"reason": "insufficient_balance"})
		}
	}
	t, replayed, err := tx.PostLedger(kind, key, debit, credit, amount, u.ID, nil, reason)
	if err != nil {
		return nil, nil, err
	}
	return &api.LedgerOperationResult{TransactionId: t.ID, Replayed: replayed}, nil, nil
}

func (h *Handlers) CreateManualLedgerEntry(ctx context.Context, req api.CreateManualLedgerEntryRequestObject) (api.CreateManualLedgerEntryResponseObject, error) {
	b := req.Body
	debit, err := storeAccount(b.Debit)
	if err != nil {
		return nil, err
	}
	credit, err := storeAccount(b.Credit)
	if err != nil {
		return nil, err
	}
	res, a, err := h.ledgerOp(ctx, "manual", req.Params.IdempotencyKey, debit, credit, b.Amount, b.Reason)
	switch {
	case err != nil:
		return nil, err
	case a != nil:
		return api.CreateManualLedgerEntry202JSONResponse{ApprovalRequiredJSONResponse: api.ApprovalRequiredJSONResponse(*a)}, nil
	}
	return api.CreateManualLedgerEntry201JSONResponse(*res), nil
}

func (h *Handlers) CreateDepositTopup(ctx context.Context, req api.CreateDepositTopupRequestObject) (api.CreateDepositTopupResponseObject, error) {
	b := req.Body
	res, a, err := h.ledgerOp(ctx, "deposit_topup", req.Params.IdempotencyKey, store.LedgerAccount{Kind: "rocketwork_deposit"},
		store.LedgerAccount{Kind: "bank"}, b.Amount, b.Reason)
	switch {
	case err != nil:
		return nil, err
	case a != nil:
		return api.CreateDepositTopup202JSONResponse{ApprovalRequiredJSONResponse: api.ApprovalRequiredJSONResponse(*a)}, nil
	}
	return api.CreateDepositTopup201JSONResponse(*res), nil
}

func (h *Handlers) CreateBalanceReturn(ctx context.Context, req api.CreateBalanceReturnRequestObject) (api.CreateBalanceReturnResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	_, err := tx.Org(req.Id)
	done()
	if err != nil {
		return nil, err
	}
	id := req.Id
	res, _, err := h.ledgerOp(ctx, "balance_return", req.Params.IdempotencyKey, store.LedgerAccount{Kind: "advertiser_balance", OrgID: &id},
		store.LedgerAccount{Kind: "bank"}, req.Body.Amount, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	return api.CreateBalanceReturn201JSONResponse(*res), nil
}

// ---------------------------------------------------------------- подтверждения

func approval(tx *store.Tx, a *store.Approval) api.AdminApproval {
	return api.AdminApproval{Id: a.ID, Kind: api.AdminApprovalKind(a.Kind), Status: api.AdminApprovalStatus(a.Status),
		Amount: a.Amount, Summary: a.Summary, ObjectId: a.ObjectID, Reason: a.Reason, RequestedBy: userRef(tx, a.RequestedBy),
		DecidedBy: userRefPtr(tx, a.DecidedBy), DecisionReason: strPtr(a.DecisionReason), ResultId: a.ResultID,
		CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt}
}

func (h *Handlers) ListAdminApprovals(ctx context.Context, req api.ListAdminApprovalsRequestObject) (api.ListAdminApprovalsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	tx.ExpireApprovals()
	q := req.Params
	status := "pending"
	if q.Status != nil {
		status = string(*q.Status)
	}
	var found []*store.Approval
	for _, a := range tx.D.Approvals {
		if a.Status == status {
			found = append(found, a)
		}
	}
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.AdminApproval{}
	for _, a := range pg {
		items = append(items, approval(tx, a))
	}
	return api.ListAdminApprovals200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) ApproveAdminApproval(ctx context.Context, req api.ApproveAdminApprovalRequestObject) (api.ApproveAdminApprovalResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	a, err := tx.Approval(req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.Approve(a, u.ID); err != nil {
		return nil, err
	}
	h.logger(ctx).Warn("admin approval approved", "op", "Admin.Approve", "approval_id", a.ID, "kind", a.Kind, "admin_user_id", u.ID)
	return api.ApproveAdminApproval200JSONResponse(approval(tx, a)), nil
}

func (h *Handlers) RejectAdminApproval(ctx context.Context, req api.RejectAdminApprovalRequestObject) (api.RejectAdminApprovalResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	a, err := tx.Approval(req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.RejectApproval(a, u.ID, req.Body.Reason); err != nil {
		return nil, err
	}
	return api.RejectAdminApproval200JSONResponse(approval(tx, a)), nil
}
