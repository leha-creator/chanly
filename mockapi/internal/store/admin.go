package store

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Операции администратора (этап «API администратора»): ручные переходы размещений, споры и
// переписка, блокировки, справочники, деньги со вторым подтверждением, ОРД. Правила — как у
// бэкенда в упрощённом виде; внешних систем нет.

// Violation — нарушение условий размещения (03 §6).
type Violation struct {
	Kind       string // removed_early | content_modified | top_violated | bot_access_lost
	OccurredAt time.Time
	Source     string // event | reconciliation
}

// DisputeMessage — сообщение переписки по спору (R-9).
type DisputeMessage struct {
	ID           uuid.UUID
	PlacementID  uuid.UUID
	AuthorType   string // admin | advertiser | blogger
	AuthorUserID uuid.UUID
	Recipient    string // у сообщения администратора: advertiser | blogger | both
	Text         string
	CreatedAt    time.Time
}

// VisibleTo — видит ли сторона сообщение: своё или адресованное ей администратором.
func (m *DisputeMessage) VisibleTo(side string) bool {
	if m.AuthorType == side {
		return true
	}
	return m.AuthorType == "admin" && (m.Recipient == side || m.Recipient == "both")
}

// Approval — запрос второго подтверждения (R-2).
type Approval struct {
	ID             uuid.UUID
	Kind           string // ledger_manual | deposit_topup | payout_mark_paid
	Status         string // pending | approved | rejected | expired
	Amount         int64
	Summary        string
	ObjectID       *uuid.UUID
	Reason         string
	Key            string
	RequestedBy    uuid.UUID
	DecidedBy      *uuid.UUID
	DecisionReason string
	ResultID       *uuid.UUID
	CreatedAt      time.Time
	ExpiresAt      time.Time
	DecidedAt      *time.Time
	// Payload — что исполнить после подтверждения.
	Debit, Credit LedgerAccount
	BankReference string
}

// AuditEntry — запись журнала действий.
type AuditEntry struct {
	ID         uuid.UUID
	ActorType  string
	ActorID    *uuid.UUID
	OrgID      *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   string
	Reason     string
	Data       map[string]any
	CreatedAt  time.Time
}

// BankStatement — загруженная выписка (S-30).
type BankStatement struct {
	ID             uuid.UUID
	UploadedBy     uuid.UUID
	AccountNumber  string
	DateFrom       time.Time
	DateTo         time.Time
	OpeningBalance int64
	ClosingBalance int64
	Incoming       int
	Matched        int
	Unmatched      int
	Duplicate      int
	CreatedAt      time.Time
}

// BankTx — поступление из выписки.
type BankTx struct {
	ID            uuid.UUID
	StatementID   uuid.UUID
	OperationDate time.Time
	Amount        int64
	PayerName     string
	PayerINN      string
	PayerAccount  string
	Purpose       string
	MatchStatus   string // matched | unmatched | ignored
	OrgID         *uuid.UUID
	InvoiceID     *uuid.UUID
	MatchedBy     *uuid.UUID
	IgnoredReason string
	CreatedAt     time.Time
}

// LedgerAccount — счёт плана счетов (06 §2.1).
type LedgerAccount struct {
	Kind        string
	OrgID       *uuid.UUID
	PlacementID *uuid.UUID
}

// AdminLedgerTx — проводка журнала денег, как её видит администратор.
type AdminLedgerTx struct {
	ID          uuid.UUID
	Kind        string
	Amount      int64
	Debit       LedgerAccount
	Credit      LedgerAccount
	ActorType   string
	ActorID     *uuid.UUID
	ApproverID  *uuid.UUID
	Reason      string
	Key         string
	PlacementID *uuid.UUID
	InvoiceID   *uuid.UUID
	PayoutID    *uuid.UUID
	CreatedAt   time.Time
}

// OrdObject — объект синхронизации с ОРД (04 §6).
type OrdObject struct {
	Type       string // organization | contract | platform | creative | statistics | invoice
	ID         uuid.UUID
	Title      string
	ExternalID string
	Status     string // pending | sent | synced | error
	OrdStatus  string
	Erid       string
	Error      string
	Attempts   int
	UpdatedAt  time.Time
	Request    map[string]any
	Response   map[string]any
}

// OrdReport — отчёт ОРД за месяц (05 §3.6, §3.7).
type OrdReport struct {
	Period          string // YYYY-MM
	BuiltAt         *time.Time
	SendRequestedAt *time.Time
	Statistics      []*OrdStatisticsRow
	Invoices        []*OrdInvoiceRow
}

type OrdStatisticsRow struct {
	ID          uuid.UUID
	PlacementID uuid.UUID
	CreativeID  uuid.UUID
	Erid        string
	ChannelID   uuid.UUID
	Impressions int64
	Amount      int64
	ShownFrom   time.Time
	ShownTo     time.Time
	Status      string
	Error       string
	Attempts    int
}

type OrdInvoiceRow struct {
	ID           uuid.UUID
	ContractKind string // initial | publisher
	OrgID        uuid.UUID
	Number       string
	InvoiceDate  time.Time
	Amount       int64
	ItemsCount   int
	Status       string
	Error        string
	Attempts     int
}

// ---------------------------------------------------------------- пороги

// SecondApprovalThreshold — порог второго подтверждения, копейки (LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD, 100 000 ₽).
const SecondApprovalThreshold int64 = 10_000_000

// ApprovalTTL — срок запроса подтверждения (ADMIN_APPROVAL_TTL).
const ApprovalTTL = 72 * time.Hour

// ---------------------------------------------------------------- журнал действий

// Audit пишет запись журнала действий администратора.
func (tx *Tx) Audit(actorID uuid.UUID, orgID *uuid.UUID, action, objectType, objectID, reason string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	id := actorID
	tx.D.Audit = append(tx.D.Audit, &AuditEntry{ID: NewID(), ActorType: "admin", ActorID: &id, OrgID: orgID,
		Action: action, ObjectType: objectType, ObjectID: objectID, Reason: reason, Data: data, CreatedAt: tx.Now})
}

// ---------------------------------------------------------------- размещения

// livePublication — пост опубликован и не снят.
func livePublication(p *Placement) bool { return p.PublishedAt != nil && p.RemovedAt == nil }

// orderOrg — организация рекламодателя размещения.
func (tx *Tx) orderOrg(p *Placement) *uuid.UUID {
	if o, err := tx.Order(p.OrderID); err == nil {
		id := o.OrgID
		return &id
	}
	return nil
}

// AdminCancel — admin_cancel (P-06 с актором admin) до публикации.
func (tx *Tx) AdminCancel(adminID uuid.UUID, p *Placement, reason string) error {
	if err := tx.apply(p, "P-06:admin", "admin_cancel"); err != nil {
		return err
	}
	p.PublishAt, p.RemoveAt = nil, nil
	tx.Audit(adminID, tx.orderOrg(p), "placement.admin_cancel", "placement", p.ID.String(), reason, nil)
	return nil
}

// AdminMarkRemoved — admin_mark_removed (P-10 с актором admin): пост считается снятым в at.
func (tx *Tx) AdminMarkRemoved(adminID uuid.UUID, p *Placement, at time.Time, reason string) error {
	if !Allowed("P-10:admin", p.Status) {
		return tx.apply(p, "P-10:admin", "")
	}
	if p.PublishedAt == nil || at.Before(*p.PublishedAt) || at.After(tx.Now) {
		return Invalid("removed_at", "removed_at_invalid", "removed_at must be between publication and now")
	}
	dl := at.Add(24 * time.Hour) // T-05 от времени снятия
	p.RemovedAt, p.RemovalReason, p.ClaimDeadlineAt = &at, "admin", &dl
	if err := tx.apply(p, "P-10:admin", "admin_mark_removed"); err != nil {
		return err
	}
	tx.Audit(adminID, tx.orderOrg(p), "placement.admin_mark_removed", "placement", p.ID.String(), reason,
		map[string]any{"removed_at": at.Format(time.RFC3339)})
	return nil
}

// AdminOpenDispute — admin_open_dispute (P-14 с актором admin, причина admin).
func (tx *Tx) AdminOpenDispute(adminID uuid.UUID, p *Placement, reason string) error {
	if err := tx.apply(p, "P-14:admin", "admin"); err != nil {
		return err
	}
	p.Dispute = &Dispute{ID: NewID(), Reason: "admin", OpenedByType: "admin", OpenedAt: tx.Now, DeadlineAt: tx.Now.Add(DisputeDeadline)}
	tx.Audit(adminID, tx.orderOrg(p), "dispute.opened", "dispute", p.Dispute.ID.String(), reason, nil)
	return nil
}

// AdminRemovePost — admin_remove_post: бот удаляет живой пост; из published сначала открывается спор.
func (tx *Tx) AdminRemovePost(adminID uuid.UUID, p *Placement, reason string) error {
	if !livePublication(p) {
		return ConflictReason("publication_not_live", "publication is not live", map[string]any{"status": p.Status})
	}
	if p.Status == "published" {
		if err := tx.AdminOpenDispute(adminID, p, reason); err != nil {
			return err
		}
	} else if p.Status != "disputed" {
		return Conflict("invalid_transition", "post can be removed only from published or disputed",
			map[string]any{"status": p.Status})
	}
	now := tx.Now
	p.RemovedAt, p.RemovalReason = &now, "admin"
	tx.Audit(adminID, tx.orderOrg(p), "dispute.remove_post", "dispute", p.Dispute.ID.String(), reason, nil)
	tx.log.Info("publication removed by admin", "op", "Placement.AdminRemovePost", "placement_id", p.ID)
	return nil
}

// Distribution — распределение удержания по доле исполнения (03 §7).
type Distribution struct {
	Share                            int
	Payout, Fee, Refund, TotalAmount int64
}

// Distribute считает доли: блогеру и комиссии — пропорционально, остальное — возврат.
func Distribute(p *Placement, share int) Distribution {
	payout := p.BloggerAmount * int64(share) / 10000
	fee := p.FeeAmount * int64(share) / 10000
	return Distribution{Share: share, Payout: payout, Fee: fee, Refund: p.TotalAmount - payout - fee, TotalAmount: p.TotalAmount}
}

// ResolutionShare — доля исполнения решения; для proportional без доли — предложение системы.
func ResolutionShare(d *Dispute, resolution string, share *int) (int, error) {
	switch resolution {
	case "full_payout":
		return 10000, nil
	case "full_refund":
		return 0, nil
	}
	if share != nil {
		if *share < 1 || *share > 9999 {
			return 0, Invalid("share", "share_out_of_range", "share must be 1..9999")
		}
		return *share, nil
	}
	if d.SuggestedShare != nil {
		return *d.SuggestedShare, nil
	}
	return 0, Invalid("share", "share_required", "share is required: no system suggestion")
}

// ResolveDispute — решение администратора (P-15).
func (tx *Tx) ResolveDispute(adminID uuid.UUID, p *Placement, resolution string, share *int, comment string) error {
	d := p.Dispute
	if d == nil {
		return NotFound("dispute", p.ID)
	}
	if d.Resolution != "" {
		return ConflictReason("dispute_resolved", "dispute is already resolved", nil)
	}
	s, err := ResolutionShare(d, resolution, share)
	if err != nil {
		return err
	}
	if s > 0 && livePublication(p) {
		return ConflictReason("publication_live", "publication is still live", nil)
	}
	if s == 0 && livePublication(p) {
		now := tx.Now
		p.RemovedAt, p.RemovalReason = &now, "refund"
	}
	dist := Distribute(p, s)
	code := "P-15:accepted"
	if s == 0 {
		code = "P-15:refunded"
	}
	if err := tx.apply(p, code, "dispute_"+resolution); err != nil {
		return err
	}
	now := tx.Now
	id := adminID
	d.Resolution, d.ResolvedAt, d.ResolvedBy, d.Comment, d.PerformanceShare = resolution, &now, &id, comment, &s
	p.PerformanceShare, p.PayoutAmount, p.FeeCharged, p.RefundAmount = &s, &dist.Payout, &dist.Fee, &dist.Refund
	p.HoldActive = false
	if o, err := tx.Order(p.OrderID); err == nil && dist.Refund > 0 {
		oid, pid := o.ID, p.ID
		tx.addEntry(o.OrgID, "refund", dist.Refund, fmt.Sprintf("Возврат по решению спора: заказ %d", o.Number), &oid, &pid, nil)
	}
	if dist.Payout > 0 {
		tx.D.Payouts = append(tx.D.Payouts, &Payout{ID: NewID(), PlacementID: p.ID, OrgID: p.BloggerOrgID,
			Amount: dist.Payout, Status: "created", Attempt: 1, CreatedAt: tx.Now})
	}
	tx.Audit(adminID, tx.orderOrg(p), "dispute.resolved", "dispute", d.ID.String(), comment,
		map[string]any{"resolution": resolution, "performance_share": s})
	return nil
}

// PostDisputeMessage добавляет сообщение в переписку открытого спора.
func (tx *Tx) PostDisputeMessage(p *Placement, authorType string, authorID uuid.UUID, recipient, text string) (*DisputeMessage, error) {
	if p.Dispute == nil {
		return nil, NotFound("dispute", p.ID)
	}
	if p.Dispute.Resolution != "" {
		return nil, ConflictReason("dispute_resolved", "dispute is already resolved", nil)
	}
	m := &DisputeMessage{ID: NewID(), PlacementID: p.ID, AuthorType: authorType, AuthorUserID: authorID,
		Recipient: recipient, Text: text, CreatedAt: tx.Now}
	tx.D.DisputeMessages = append(tx.D.DisputeMessages, m)
	o, _ := tx.Order(p.OrderID)
	orderID := o.ID
	notifyAdv := func() {
		tx.Notify(o.OrgID, "dispute.message", fmt.Sprintf("Сообщение по спору, заказ № %d", o.Number),
			"Администратор просит пояснение по спору. Ответьте в карточке размещения.", "placement", p.ID, &orderID, "/adv/orders/"+o.ID.String())
	}
	notifyBlg := func() {
		tx.Notify(p.BloggerOrgID, "dispute.message", fmt.Sprintf("Сообщение по спору, заказ № %d", o.Number),
			"Администратор просит пояснение по спору. Ответьте в карточке размещения.", "placement", p.ID, &orderID, "/blg/placements?placement="+p.ID.String())
	}
	switch {
	case authorType == "admin" && recipient == "advertiser":
		notifyAdv()
	case authorType == "admin" && recipient == "blogger":
		notifyBlg()
	case authorType == "admin":
		notifyAdv()
		notifyBlg()
	}
	if authorType == "admin" {
		tx.Audit(authorID, tx.orderOrg(p), "dispute.message", "dispute", p.Dispute.ID.String(), "",
			map[string]any{"recipient": recipient})
	}
	tx.log.Info("dispute message posted", "op", "Dispute.Message", "placement_id", p.ID, "author_type", authorType, "recipient", recipient)
	return m, nil
}

// DisputeByID ищет размещение по идентификатору спора.
func (tx *Tx) DisputeByID(id uuid.UUID) (*Placement, error) {
	for _, p := range tx.D.Placements {
		if p.Dispute != nil && p.Dispute.ID == id {
			return p, nil
		}
	}
	return nil, tx.notFound("dispute", id)
}

// ---------------------------------------------------------------- каналы и справочники

// BlockChannel — block_channel: из любого состояния, кроме blocked.
func (tx *Tx) BlockChannel(adminID uuid.UUID, c *Channel, reason string) error {
	if c.Status == "blocked" {
		return ConflictReason("channel_blocked", "channel is already blocked", map[string]any{"status": c.Status})
	}
	from := c.Status
	c.Status, c.StatusReason, c.UpdatedAt = "blocked", reason, tx.Now
	tx.Audit(adminID, c.OrgID, "channel.blocked", "channel", c.ID.String(), reason, map[string]any{"from": from})
	if c.OrgID != nil {
		tx.Notify(*c.OrgID, "channel.blocked", "Канал «"+c.Title+"» заблокирован",
			"Канал заблокирован администратором: "+reason+"\nНовые размещения невозможны, начатые продолжаются.",
			"channel", c.ID, nil, "/blg/channels/"+c.ID.String())
	}
	return nil
}

// UnblockChannel снимает блокировку: канал уходит на повторную модерацию.
func (tx *Tx) UnblockChannel(adminID uuid.UUID, c *Channel, reason string) error {
	if c.Status != "blocked" {
		return ConflictReason("channel_not_blocked", "channel is not blocked", map[string]any{"status": c.Status})
	}
	c.Status, c.StatusReason, c.UpdatedAt = "moderation", "", tx.Now
	tx.Audit(adminID, c.OrgID, "channel.unblocked", "channel", c.ID.String(), reason, nil)
	return nil
}

// ---------------------------------------------------------------- выплаты

// LastPayout — последняя попытка выплаты размещения.
func (tx *Tx) LastPayout(placementID uuid.UUID) *Payout {
	var last *Payout
	for _, po := range tx.D.Payouts {
		if po.PlacementID == placementID && (last == nil || po.Attempt > last.Attempt) {
			last = po
		}
	}
	return last
}

// PayoutByID ищет выплату.
func (tx *Tx) PayoutByID(id uuid.UUID) (*Payout, error) {
	for _, po := range tx.D.Payouts {
		if po.ID == id {
			return po, nil
		}
	}
	return nil, tx.notFound("payout", id)
}

// RetryPayout — retry_payout: новая попытка после failed последней попытки размещения в accepted.
func (tx *Tx) RetryPayout(adminID uuid.UUID, po *Payout, reason string) (*Payout, error) {
	p, err := tx.Placement(po.PlacementID)
	if err != nil {
		return nil, err
	}
	if po.Status != "failed" || tx.LastPayout(p.ID) != po || p.Status != "accepted" {
		return nil, ConflictReason("payout_not_retryable", "only the last failed attempt of an accepted placement can be retried",
			map[string]any{"status": po.Status})
	}
	n := &Payout{ID: NewID(), PlacementID: p.ID, OrgID: po.OrgID, Amount: po.Amount, Status: "created",
		Attempt: po.Attempt + 1, Method: po.Method, CreatedAt: tx.Now}
	tx.D.Payouts = append(tx.D.Payouts, n)
	tx.Audit(adminID, &po.OrgID, "payout.retry", "payout", n.ID.String(), reason, map[string]any{"previous_payout_id": po.ID.String()})
	return n, nil
}

// MarkPayoutPaid отмечает выплату юрлицу переводом; approverID — второй администратор выше порога.
func (tx *Tx) MarkPayoutPaid(adminID uuid.UUID, approverID *uuid.UUID, po *Payout, bankRef, reason string) error {
	if po.Method != "bank_transfer" {
		return ConflictReason("payout_not_bank_transfer", "payout is not a bank transfer", nil)
	}
	if po.Status == "succeeded" {
		return ConflictReason("payout_already_paid", "payout is already paid", nil)
	}
	now := tx.Now
	po.Status, po.CompletedAt, po.BankReference = "succeeded", &now, bankRef
	data := map[string]any{"bank_reference": bankRef}
	if approverID != nil {
		data["approver_id"] = approverID.String()
	}
	tx.Audit(adminID, &po.OrgID, "payout.mark_paid", "payout", po.ID.String(), reason, data)
	if p, err := tx.Placement(po.PlacementID); err == nil && p.Status == "accepted" {
		return tx.apply(p, "P-16", "")
	}
	return nil
}

// ---------------------------------------------------------------- журнал денег

// accountKey — строка для сравнения счетов.
func (a LedgerAccount) key() string {
	s := a.Kind
	if a.OrgID != nil {
		s += ":" + a.OrgID.String()
	}
	if a.PlacementID != nil {
		s += ":" + a.PlacementID.String()
	}
	return s
}

// activeKinds — активы (06 §2.1): остаток растёт по дебету.
var activeKinds = []string{"bank", "rocketwork_deposit", "payout_fees", "diagnostic_expense"}

// LedgerBalances — остатки по видам счетов.
func (tx *Tx) LedgerBalances() map[string]int64 {
	out := map[string]int64{}
	for _, t := range tx.D.AdminLedger {
		if slices.Contains(activeKinds, t.Debit.Kind) {
			out[t.Debit.Kind] += t.Amount
		} else {
			out[t.Debit.Kind] -= t.Amount
		}
		if slices.Contains(activeKinds, t.Credit.Kind) {
			out[t.Credit.Kind] -= t.Amount
		} else {
			out[t.Credit.Kind] += t.Amount
		}
	}
	return out
}

// PostLedger создаёт проводку; повтор ключа возвращает прежнюю (replayed).
func (tx *Tx) PostLedger(kind, key string, debit, credit LedgerAccount, amount int64, adminID uuid.UUID, approverID *uuid.UUID, reason string) (*AdminLedgerTx, bool, error) {
	for _, t := range tx.D.AdminLedger {
		if key != "" && t.Key == key {
			return t, true, nil
		}
	}
	if debit.key() == credit.key() {
		return nil, false, Invalid("credit", "same_account", "debit and credit accounts must differ")
	}
	id := adminID
	t := &AdminLedgerTx{ID: NewID(), Kind: kind, Amount: amount, Debit: debit, Credit: credit, ActorType: "admin",
		ActorID: &id, ApproverID: approverID, Reason: reason, Key: key, CreatedAt: tx.Now}
	tx.D.AdminLedger = append(tx.D.AdminLedger, t)
	data := map[string]any{"amount": amount, "debit": debit.Kind, "credit": credit.Kind}
	if approverID != nil {
		data["approver_id"] = approverID.String()
	}
	tx.Audit(adminID, debit.OrgID, "ledger."+kind, "ledger_transaction", t.ID.String(), reason, data)
	if debit.Kind == "advertiser_balance" && debit.OrgID != nil {
		tx.addEntry(*debit.OrgID, kind, -amount, "Корректировка администратора", nil, nil, nil)
	}
	if credit.Kind == "advertiser_balance" && credit.OrgID != nil {
		tx.addEntry(*credit.OrgID, kind, amount, "Корректировка администратора", nil, nil, nil)
	}
	tx.log.Warn("manual ledger operation", "op", "Ledger.Post", "kind", kind, "amount", amount, "admin_user_id", adminID)
	return t, false, nil
}

// ---------------------------------------------------------------- подтверждения

// ApprovalByKey — запрос с тем же ключом идемпотентности.
func (tx *Tx) ApprovalByKey(key string) *Approval {
	for _, a := range tx.D.Approvals {
		if a.Key == key {
			return a
		}
	}
	return nil
}

// RequestApproval создаёт запрос второго подтверждения; повтор ключа — тот же запрос.
func (tx *Tx) RequestApproval(a *Approval) *Approval {
	if prev := tx.ApprovalByKey(a.Key); prev != nil {
		return prev
	}
	a.ID, a.Status, a.CreatedAt, a.ExpiresAt = NewID(), "pending", tx.Now, tx.Now.Add(ApprovalTTL)
	tx.D.Approvals = append(tx.D.Approvals, a)
	tx.Audit(a.RequestedBy, nil, "admin_approval.requested", "admin_approval", a.ID.String(), a.Reason,
		map[string]any{"kind": a.Kind, "amount": a.Amount})
	tx.log.Warn("admin approval requested", "op", "Approval.Request", "approval_id", a.ID, "kind", a.Kind, "amount", a.Amount)
	return a
}

// Approval ищет запрос; просроченный pending переводится в expired.
func (tx *Tx) Approval(id uuid.UUID) (*Approval, error) {
	for _, a := range tx.D.Approvals {
		if a.ID == id {
			tx.expire(a)
			return a, nil
		}
	}
	return nil, tx.notFound("admin_approval", id)
}

func (tx *Tx) expire(a *Approval) {
	if a.Status == "pending" && !tx.Now.Before(a.ExpiresAt) {
		a.Status = "expired"
	}
}

// ExpireApprovals переводит просроченные запросы в expired.
func (tx *Tx) ExpireApprovals() {
	for _, a := range tx.D.Approvals {
		tx.expire(a)
	}
}

// Approve исполняет операцию запроса с подтверждением другого senior.
func (tx *Tx) Approve(a *Approval, approverID uuid.UUID) error {
	if err := decidable(a); err != nil {
		return err
	}
	if a.RequestedBy == approverID {
		return ConflictReason("same_admin", "approval must be decided by another admin", nil)
	}
	var result uuid.UUID
	switch a.Kind {
	case "ledger_manual", "deposit_topup":
		kind := "manual"
		if a.Kind == "deposit_topup" {
			kind = "deposit_topup"
		}
		t, _, err := tx.PostLedger(kind, a.Key, a.Debit, a.Credit, a.Amount, a.RequestedBy, &approverID, a.Reason)
		if err != nil {
			return err
		}
		result = t.ID
	case "payout_mark_paid":
		po, err := tx.PayoutByID(*a.ObjectID)
		if err != nil {
			return err
		}
		if err := tx.MarkPayoutPaid(a.RequestedBy, &approverID, po, a.BankReference, a.Reason); err != nil {
			return err
		}
		result = po.ID
	}
	now := tx.Now
	a.Status, a.DecidedBy, a.DecidedAt, a.ResultID = "approved", &approverID, &now, &result
	tx.Audit(approverID, nil, "admin_approval.approved", "admin_approval", a.ID.String(), "", map[string]any{"kind": a.Kind})
	return nil
}

// RejectApproval отклоняет запрос (автор может отозвать свой).
func (tx *Tx) RejectApproval(a *Approval, adminID uuid.UUID, reason string) error {
	if err := decidable(a); err != nil {
		return err
	}
	now := tx.Now
	a.Status, a.DecidedBy, a.DecidedAt, a.DecisionReason = "rejected", &adminID, &now, reason
	tx.Audit(adminID, nil, "admin_approval.rejected", "admin_approval", a.ID.String(), reason, map[string]any{"kind": a.Kind})
	return nil
}

func decidable(a *Approval) error {
	switch a.Status {
	case "pending":
		return nil
	case "expired":
		return ConflictReason("approval_expired", "approval request has expired", nil)
	}
	return ConflictReason("approval_decided", "approval request is already decided", map[string]any{"status": a.Status})
}

// ---------------------------------------------------------------- пользователи

// BlockUser блокирует пользователя и закрывает его сессии.
func (tx *Tx) BlockUser(adminID uuid.UUID, u *User, reason string) error {
	if u.AdminRole != "" {
		return ConflictReason("admin_user", "admin user cannot be blocked", nil)
	}
	if u.Blocked {
		return ConflictReason("user_blocked", "user is already blocked", nil)
	}
	now := tx.Now
	u.Blocked, u.BlockedReason, u.BlockedAt = true, reason, &now
	for token, s := range tx.s.sessions {
		if s.UserID == u.ID {
			delete(tx.s.sessions, token)
		}
	}
	tx.Audit(adminID, u.OrgID, "user.blocked", "user", u.ID.String(), reason, nil)
	return nil
}

// UnblockUser снимает блокировку.
func (tx *Tx) UnblockUser(adminID uuid.UUID, u *User, reason string) error {
	if !u.Blocked {
		return ConflictReason("user_not_blocked", "user is not blocked", nil)
	}
	u.Blocked, u.BlockedReason, u.BlockedAt = false, "", nil
	tx.Audit(adminID, u.OrgID, "user.unblocked", "user", u.ID.String(), reason, nil)
	return nil
}

// ---------------------------------------------------------------- ОРД

// OrdObjectByRef ищет объект ОРД.
func (tx *Tx) OrdObjectByRef(typ string, id uuid.UUID) (*OrdObject, error) {
	for _, o := range tx.D.OrdObjects {
		if o.Type == typ && o.ID == id {
			return o, nil
		}
	}
	return nil, tx.notFound("ord_object", typ+":"+id.String())
}

// OrdReportFor — отчёт за месяц; nil — не формировался.
func (tx *Tx) OrdReportFor(period string) *OrdReport {
	for _, r := range tx.D.OrdReports {
		if r.Period == period {
			return r
		}
	}
	return nil
}

// ReportState — состояние отчёта для экрана (R-21).
func ReportState(r *OrdReport) string {
	if r == nil || r.BuiltAt == nil {
		return "not_built"
	}
	pending, errs := 0, 0
	each := func(status string) {
		switch status {
		case "error":
			errs++
		case "pending", "sent":
			pending++
		}
	}
	for _, s := range r.Statistics {
		each(s.Status)
	}
	for _, i := range r.Invoices {
		each(i.Status)
	}
	switch {
	case errs > 0:
		return "has_errors"
	case pending == 0:
		return "sent"
	case r.SendRequestedAt != nil:
		return "sending"
	}
	return "built"
}
