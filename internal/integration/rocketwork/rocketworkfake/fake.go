// Package rocketworkfake — поддельный Рокет Ворк для тестов и разработки (R-1,
// ROCKETWORK_MODE=fake): исполнители, депозит и выплаты в памяти с тем же контрактом, что у
// rocketwork.Client. Тест управляет состоянием: активирует и блокирует исполнителей, задаёт
// депозит, завершает и отклоняет выплаты, подкладывает сбои.
//
// В разработке процессы api и worker видят одно состояние через JSON-файл (StatePath).
//
// Данные подделки придуманы (раздел «Допущения о внешних данных» плана): комиссия 3 %
// не меньше 1 ₽, ссылки https://rocketwork.example.test/…
package rocketworkfake

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Комиссия подделки (ПРЕДПОЛОЖЕНИЕ (Q-12): тариф сервиса не известен).
const (
	FeeRateBP = 300
	MinFee    = 100
)

// BaseURL — адрес ссылок подделки.
const BaseURL = "https://rocketwork.example.test"

// Contractor — исполнитель подделки.
type Contractor struct {
	ID            string `json:"id"`
	ExternalID    string `json:"external_id"`
	INN           string `json:"inn"`
	LegalForm     string `json:"legal_form"`
	Status        string `json:"status"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	OnboardingURL string `json:"onboarding_url"`
}

// Payment — выплата подделки.
type Payment struct {
	ID             string `json:"id"`
	ExternalID     string `json:"external_id"`
	IdempotencyKey string `json:"idempotency_key"`
	ContractorID   string `json:"contractor_id"`
	AmountKopecks  int64  `json:"amount_kopecks"`
	Purpose        string `json:"purpose"`
	Status         string `json:"status"`
	FeeKopecks     int64  `json:"fee_kopecks"`
	ReceiptURL     string `json:"receipt_url,omitempty"`
	FailureCode    string `json:"failure_code,omitempty"`
	FailureMessage string `json:"failure_message,omitempty"`
}

// Call — вызов подделки для проверок в тестах.
type Call struct {
	Operation string
	Key       string
}

// state — сохраняемая часть подделки.
type state struct {
	Seq         int                    `json:"seq"`
	Deposit     int64                  `json:"deposit_kopecks"`
	Contractors map[string]*Contractor `json:"contractors"`
	Payments    map[string]*Payment    `json:"payments"`
	// AutoActivate — исполнитель сразу active (разработка без ручных шагов).
	AutoActivate bool `json:"auto_activate"`
}

// Fake — поддельный Рокет Ворк.
type Fake struct {
	mu     sync.Mutex
	st     state
	path   string
	secret []byte
	log    *slog.Logger
	calls  []Call

	failNext    map[string]error
	timeoutNext map[string]bool
	unknownNext bool
}

// Option — настройка подделки.
type Option func(*Fake)

// WithStatePath — общее состояние процессов в JSON-файле (разработка).
func WithStatePath(path string) Option { return func(f *Fake) { f.path = path } }

// WithLogger — журнал DEBUG вызовов.
func WithLogger(log *slog.Logger) Option { return func(f *Fake) { f.log = log } }

// New создаёт подделку; secret — ключ подписи вебхуков (ROCKETWORK_WEBHOOK_SECRET).
func New(secret []byte, opts ...Option) *Fake {
	f := &Fake{
		st:     state{Contractors: map[string]*Contractor{}, Payments: map[string]*Payment{}},
		secret: secret, log: slog.New(slog.DiscardHandler),
		failNext: map[string]error{}, timeoutNext: map[string]bool{},
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Secret — ключ подписи вебхуков.
func (f *Fake) Secret() []byte { return f.secret }

// with выполняет действие над состоянием под блокировкой, с чтением и записью файла.
func (f *Fake) with(fn func(st *state) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.load(); err != nil {
		return err
	}
	if err := fn(&f.st); err != nil {
		return err
	}
	return f.save()
}

func (f *Fake) record(ctx context.Context, op, key string) {
	f.calls = append(f.calls, Call{Operation: op, Key: key})
	f.log.DebugContext(ctx, "rocketworkfake call", logx.KeyOp, "RocketWorkFake."+op, "key", key)
}

// injected — подложенный сбой операции.
func (f *Fake) injected(op string) error {
	if err, ok := f.failNext[op]; ok {
		delete(f.failNext, op)
		return err
	}
	return nil
}

func (f *Fake) nextID(st *state, prefix string) string {
	st.Seq++
	return fmt.Sprintf("%s-%d", prefix, st.Seq)
}

// ---------------------------------------------------------------- контракт адаптера

// EnsureContractor создаёт исполнителя; повтор с тем же ИНН возвращает существующего.
func (f *Fake) EnsureContractor(ctx context.Context, r rocketwork.ContractorRequest) (rocketwork.Contractor, error) {
	var out rocketwork.Contractor
	err := f.with(func(st *state) error {
		f.record(ctx, "EnsureContractor", r.ExternalID)
		if err := f.injected("EnsureContractor"); err != nil {
			return err
		}
		for _, c := range st.Contractors {
			if c.INN == r.INN {
				out = contractorOut(c)
				return nil
			}
		}
		c := &Contractor{ID: f.nextID(st, "rwc"), ExternalID: r.ExternalID, INN: r.INN, LegalForm: r.LegalForm,
			Status: "invited"}
		c.OnboardingURL = BaseURL + "/onboarding/" + c.ID
		if st.AutoActivate {
			c.Status = "active"
		}
		st.Contractors[c.ID] = c
		out = contractorOut(c)
		return nil
	})
	return out, err
}

func contractorOut(c *Contractor) rocketwork.Contractor {
	status, reason, known := rocketwork.ContractorStatusFrom(c.Status)
	if status == rocketwork.ContractorBlocked && c.BlockedReason != "" {
		reason = c.BlockedReason
	}
	return rocketwork.Contractor{ID: c.ID, Status: status, BlockedReason: reason, OnboardingURL: c.OnboardingURL,
		RawStatus: c.Status, Known: known}
}

// GetContractor — исполнитель по идентификатору подделки.
func (f *Fake) GetContractor(ctx context.Context, id string) (rocketwork.Contractor, error) {
	var out rocketwork.Contractor
	err := f.with(func(st *state) error {
		f.record(ctx, "GetContractor", id)
		if err := f.injected("GetContractor"); err != nil {
			return err
		}
		c, ok := st.Contractors[id]
		if !ok {
			return rocketwork.ErrNotFound
		}
		out = contractorOut(c)
		if f.unknownNext {
			f.unknownNext = false
			out.Status, out.Known, out.RawStatus = rocketwork.ContractorPending, false, "mystery"
		}
		return nil
	})
	return out, err
}

// GetDepositBalance — остаток депозита.
func (f *Fake) GetDepositBalance(ctx context.Context) (int64, error) {
	var out int64
	err := f.with(func(st *state) error {
		f.record(ctx, "GetDepositBalance", "")
		if err := f.injected("GetDepositBalance"); err != nil {
			return err
		}
		out = st.Deposit
		return nil
	})
	return out, err
}

// Fee — комиссия подделки за выплату.
func Fee(amount int64) int64 { return max(amount*FeeRateBP/10000, MinFee) }

// CreatePayout создаёт выплату в processing; идемпотентен по ключу. Исполнитель не active —
// отказ contractor_not_active (или tax_status_lost); депозита меньше суммы с комиссией —
// insufficient_deposit.
func (f *Fake) CreatePayout(ctx context.Context, r rocketwork.PaymentRequest) (rocketwork.Payment, error) {
	var out rocketwork.Payment
	timeout := false
	err := f.with(func(st *state) error {
		f.record(ctx, "CreatePayout", r.IdempotencyKey)
		if err := f.injected("CreatePayout"); err != nil {
			return err
		}
		for _, p := range st.Payments {
			if p.IdempotencyKey == r.IdempotencyKey {
				out = f.paymentOut(p)
				return nil
			}
		}
		c, ok := st.Contractors[r.ContractorID]
		if !ok {
			return &rocketwork.RejectedError{Code: rocketwork.FailureContractorNotActive, Message: "contractor not found"}
		}
		if c.Status != "active" {
			code := rocketwork.FailureContractorNotActive
			if c.BlockedReason == rocketwork.FailureTaxStatusLost {
				code = rocketwork.FailureTaxStatusLost
			}
			return &rocketwork.RejectedError{Code: code, Message: "contractor is " + c.Status}
		}
		if st.Deposit < r.AmountKopecks+Fee(r.AmountKopecks) {
			return &rocketwork.RejectedError{Code: rocketwork.FailureInsufficientDeposit, Message: "insufficient funds"}
		}
		p := &Payment{ID: f.nextID(st, "rwp"), ExternalID: r.ExternalID, IdempotencyKey: r.IdempotencyKey,
			ContractorID: r.ContractorID, AmountKopecks: r.AmountKopecks, Purpose: r.Purpose, Status: "processing"}
		st.Payments[p.ID] = p
		out = f.paymentOut(p)
		if f.timeoutNext["CreatePayout"] {
			delete(f.timeoutNext, "CreatePayout")
			timeout = true
		}
		return nil
	})
	if err == nil && timeout {
		// Выплата создана, но ответ «потерян» (неизвестный исход, R-6).
		return rocketwork.Payment{}, fmt.Errorf("%w: timeout", rocketwork.ErrUnavailable)
	}
	return out, err
}

func (f *Fake) paymentOut(p *Payment) rocketwork.Payment {
	status, known := rocketwork.PaymentStatusFrom(p.Status)
	out := rocketwork.Payment{ID: p.ID, ExternalID: p.ExternalID, Status: status, RawStatus: p.Status, Known: known,
		FeeKopecks: p.FeeKopecks, ReceiptURL: p.ReceiptURL, FailureMessage: p.FailureMessage}
	if status == rocketwork.PaymentFailed {
		out.FailureCode = rocketwork.FailureCodeFrom(p.FailureCode)
	}
	return out
}

// GetPayout — выплата по идентификатору подделки.
func (f *Fake) GetPayout(ctx context.Context, id string) (rocketwork.Payment, error) {
	var out rocketwork.Payment
	err := f.with(func(st *state) error {
		f.record(ctx, "GetPayout", id)
		if err := f.injected("GetPayout"); err != nil {
			return err
		}
		p, ok := st.Payments[id]
		if !ok {
			return rocketwork.ErrNotFound
		}
		out = f.paymentOut(p)
		if f.unknownNext {
			f.unknownNext = false
			out.Status, out.Known, out.RawStatus = rocketwork.PaymentSent, false, "mystery"
		}
		return nil
	})
	return out, err
}

// FindPayout — выплата по нашему ключу.
func (f *Fake) FindPayout(ctx context.Context, key string) (rocketwork.Payment, bool, error) {
	var (
		out   rocketwork.Payment
		found bool
	)
	err := f.with(func(st *state) error {
		f.record(ctx, "FindPayout", key)
		if err := f.injected("FindPayout"); err != nil {
			return err
		}
		for _, p := range st.Payments {
			if p.IdempotencyKey == key {
				out, found = f.paymentOut(p), true
			}
		}
		return nil
	})
	return out, found, err
}

// ParseWebhook проверяет подпись секретом подделки.
func (f *Fake) ParseWebhook(header map[string][]string, body []byte) (rocketwork.Event, error) {
	return rocketwork.ParseWebhook(f.secret, header, body)
}

// ---------------------------------------------------------------- управление из тестов

// SetAutoActivate — новые исполнители сразу active.
func (f *Fake) SetAutoActivate(on bool) error {
	return f.with(func(st *state) error { st.AutoActivate = on; return nil })
}

// SetDeposit задаёт остаток депозита.
func (f *Fake) SetDeposit(kopecks int64) error {
	return f.with(func(st *state) error { st.Deposit = kopecks; return nil })
}

// Deposit — остаток депозита.
func (f *Fake) Deposit() int64 {
	var out int64
	_ = f.with(func(st *state) error { out = st.Deposit; return nil })
	return out
}

func (f *Fake) setContractor(id string, fn func(c *Contractor)) error {
	return f.with(func(st *state) error {
		c, ok := st.Contractors[id]
		if !ok {
			return fmt.Errorf("rocketworkfake: contractor %s: %w", id, rocketwork.ErrNotFound)
		}
		fn(c)
		return nil
	})
}

// ActivateContractor делает исполнителя active.
func (f *Fake) ActivateContractor(id string) error {
	return f.setContractor(id, func(c *Contractor) { c.Status, c.BlockedReason = "active", "" })
}

// SetContractorStatus задаёт статус сервиса как есть (registering, documents_check …).
func (f *Fake) SetContractorStatus(id, raw string) error {
	return f.setContractor(id, func(c *Contractor) { c.Status = raw })
}

// BlockContractor блокирует исполнителя; reason tax_status_lost — утрата статуса самозанятого.
func (f *Fake) BlockContractor(id, reason string) error {
	return f.setContractor(id, func(c *Contractor) {
		c.Status, c.BlockedReason = "blocked", reason
		if reason == rocketwork.FailureTaxStatusLost {
			c.Status = "fns_unbound"
		}
	})
}

// ContractorByINN — идентификатор исполнителя по ИНН (команды разработчика).
func (f *Fake) ContractorByINN(inn string) (Contractor, bool) {
	var (
		out Contractor
		ok  bool
	)
	_ = f.with(func(st *state) error {
		for _, c := range st.Contractors {
			if c.INN == inn {
				out, ok = *c, true
			}
		}
		return nil
	})
	return out, ok
}

// Payments — копии всех выплат.
func (f *Fake) Payments() []Payment {
	var out []Payment
	_ = f.with(func(st *state) error {
		for _, p := range st.Payments {
			out = append(out, *p)
		}
		return nil
	})
	return out
}

// PaymentByExternalID — выплата по нашему идентификатору попытки.
func (f *Fake) PaymentByExternalID(ext string) (Payment, bool) {
	for _, p := range f.Payments() {
		if p.ExternalID == ext {
			return p, true
		}
	}
	return Payment{}, false
}

// CompletePayout переводит выплату в paid: списывает сумму и комиссию с депозита, выдаёт чек.
// fee < 0 — комиссия по тарифу подделки.
func (f *Fake) CompletePayout(id string, fee int64) error {
	return f.with(func(st *state) error {
		p, ok := st.Payments[id]
		if !ok {
			return fmt.Errorf("rocketworkfake: payment %s: %w", id, rocketwork.ErrNotFound)
		}
		if p.Status == "paid" {
			return nil
		}
		if fee < 0 {
			fee = Fee(p.AmountKopecks)
		}
		p.Status, p.FeeKopecks, p.ReceiptURL = "paid", fee, BaseURL+"/receipts/"+p.ID
		st.Deposit -= p.AmountKopecks + fee
		return nil
	})
}

// FailPayout отклоняет выплату с причиной сервиса (insufficient_funds, fns_unbound …).
func (f *Fake) FailPayout(id, reason, message string) error {
	return f.with(func(st *state) error {
		p, ok := st.Payments[id]
		if !ok {
			return fmt.Errorf("rocketworkfake: payment %s: %w", id, rocketwork.ErrNotFound)
		}
		p.Status, p.FailureCode, p.FailureMessage = "failed", reason, message
		return nil
	})
}

// FailNext — следующий вызов операции вернёт err.
func (f *Fake) FailNext(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[op] = err
}

// TimeoutNext — следующий CreatePayout создаст выплату, но вернёт временную ошибку.
func (f *Fake) TimeoutNext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timeoutNext["CreatePayout"] = true
}

// UnknownStatusNext — следующий GetContractor или GetPayout вернёт незнакомый статус.
func (f *Fake) UnknownStatusNext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unknownNext = true
}

// Calls — вызовы подделки по порядку.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CountCalls — сколько раз вызвана операция.
func (f *Fake) CountCalls(op string) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Operation == op {
			n++
		}
	}
	return n
}

// SignedWebhook — подписанный вебхук события объекта: kind — rocketwork.EventContractorStatus
// или EventPaymentStatus, objectID — идентификатор подделки.
func (f *Fake) SignedWebhook(kind, objectID string) (http.Header, []byte) {
	event := rocketwork.ServiceEventPayment
	if kind == rocketwork.EventContractorStatus {
		event = rocketwork.ServiceEventContractor
	}
	f.mu.Lock()
	f.st.Seq++
	eventID := fmt.Sprintf("rwe-%d-%d", f.st.Seq, time.Now().UnixNano())
	f.mu.Unlock()
	body := rocketwork.WebhookPayload(eventID, event, objectID)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(rocketwork.SignatureHeader, rocketwork.Sign(f.secret, body))
	return h, body
}
