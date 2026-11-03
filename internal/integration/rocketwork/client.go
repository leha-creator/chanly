package rocketwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// Адрес и лимит по умолчанию.
//
// ПРЕДПОЛОЖЕНИЕ (Q-10): базовый адрес API и версия; 5 запросов в секунду на заказчика.
const (
	DefaultBaseURL = "https://api.rocketwork.ru/api/v2"
	DefaultRate    = 5.0
)

// Операции адаптера: имена в outbound_request.
const (
	OpEnsureContractor = "EnsureContractor"
	OpGetContractor    = "GetContractor"
	OpGetDeposit       = "GetDepositBalance"
	OpCreatePayout     = "CreatePayout"
	OpGetPayout        = "GetPayout"
	OpFindPayout       = "FindPayout"
)

// busyRetry — через сколько повторить задание, если ограничитель занят.
const busyRetry = 30 * time.Second

// Options — подключение к Рокет Ворк.
type Options struct {
	BaseURL string
	// Token — токен кабинета заказчика (секрет, 07 §4).
	Token string
	// WebhookSecret — ключ подписи вебхуков (ROCKETWORK_WEBHOOK_SECRET).
	WebhookSecret []byte
	Rate          float64
}

// Client — HTTP-клиент Рокет Ворк по предполагаемому контракту (ROCKETWORK_MODE=live).
// Каждый запрос проходит общий ограничитель (rocketwork:global) и platform/outbound: повторы,
// предохранитель, outbound_request с маскированными ИНН, ФИО и телефоном.
type Client struct {
	out     *outbound.Client
	limiter *ratelimit.Limiter
	opts    Options
	log     *slog.Logger
}

// New создаёт клиент. out — клиент outbound системы rocketwork.
func New(out *outbound.Client, limiter *ratelimit.Limiter, opts Options, log *slog.Logger) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.Rate <= 0 {
		opts.Rate = DefaultRate
	}
	return &Client{out: out, limiter: limiter, opts: opts, log: log.With(logx.KeyComponent, "rocketwork")}
}

// ---------------------------------------------------------------- формат сервиса

// ПРЕДПОЛОЖЕНИЕ (Q-10): тело исполнителя {id, status, onboarding_url, blocked_reason}.
type contractorDTO struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	OnboardingURL string `json:"onboarding_url"`
	BlockedReason string `json:"blocked_reason"`
}

// ПРЕДПОЛОЖЕНИЕ (Q-10): запрос создания исполнителя.
type contractorCreateDTO struct {
	ExternalID string `json:"external_id"`
	INN        string `json:"inn"`
	Phone      string `json:"phone"`
	FullName   string `json:"full_name"`
	LegalForm  string `json:"legal_form"`
}

// ПРЕДПОЛОЖЕНИЕ (Q-10): тело выплаты.
type paymentDTO struct {
	ID             string `json:"id"`
	ExternalID     string `json:"external_id"`
	Status         string `json:"status"`
	AmountKopecks  int64  `json:"amount_kopecks"`
	FeeKopecks     int64  `json:"fee_kopecks"`
	ReceiptURL     string `json:"receipt_url"`
	FailureReason  string `json:"failure_reason"`
	FailureMessage string `json:"failure_message"`
}

// ПРЕДПОЛОЖЕНИЕ (Q-10): запрос создания выплаты.
type paymentCreateDTO struct {
	ContractorID  string `json:"contractor_id"`
	AmountKopecks int64  `json:"amount_kopecks"`
	Purpose       string `json:"purpose"`
	ExternalID    string `json:"external_id"`
}

// ПРЕДПОЛОЖЕНИЕ (Q-10): тело ошибки {error: {code, message}}.
type errorDTO struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (d contractorDTO) ours() Contractor {
	status, reason, known := ContractorStatusFrom(d.Status)
	if status == ContractorBlocked && d.BlockedReason != "" && reason != FailureTaxStatusLost {
		reason = d.BlockedReason
	}
	return Contractor{ID: d.ID, Status: status, BlockedReason: reason, OnboardingURL: d.OnboardingURL,
		RawStatus: d.Status, Known: known}
}

func (d paymentDTO) ours() Payment {
	status, known := PaymentStatusFrom(d.Status)
	p := Payment{ID: d.ID, ExternalID: d.ExternalID, Status: status, FeeKopecks: d.FeeKopecks, ReceiptURL: d.ReceiptURL,
		FailureMessage: d.FailureMessage, RawStatus: d.Status, Known: known}
	if status == PaymentFailed {
		p.FailureCode = FailureCodeFrom(d.FailureReason)
	}
	return p
}

// ---------------------------------------------------------------- вызовы

type request struct {
	operation string
	method    string
	path      string
	query     url.Values
	body      any
	key       string
	objectID  string
	create    bool
	check     func(ctx context.Context) (bool, error)
}

func (c *Client) bucket() ratelimit.Bucket {
	return ratelimit.Bucket{Key: "rocketwork:global", Rate: c.opts.Rate, Burst: c.opts.Rate}
}

// do выполняет вызов и раскладывает результат в out; ошибки — в наших терминах (classify).
func (c *Client) do(ctx context.Context, r request, out any) error {
	if err := c.limiter.Wait(ctx, c.bucket(), ratelimit.PriorityPublish); err != nil {
		if errors.Is(err, ratelimit.ErrBusy) {
			return job.Retry(err, busyRetry)
		}
		return err
	}
	u := c.opts.BaseURL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	call := outbound.Call{
		Operation: "RocketWork." + r.operation,
		Method:    r.method,
		URL:       u,
		// ПРЕДПОЛОЖЕНИЕ (Q-10): постоянный токен кабинета заказчика в Authorization: Bearer.
		Header:     http.Header{"Authorization": {"Bearer " + c.opts.Token}},
		Body:       r.body,
		ObjectType: r.operation,
		ObjectID:   r.objectID,
		Create:     r.create, CheckExisting: r.check,
	}
	if r.key != "" {
		// ПРЕДПОЛОЖЕНИЕ (Q-10): ключ идемпотентности в заголовке Idempotency-Key.
		call.IdempotencyKey, call.IdempotencyHeader = r.key, "Idempotency-Key"
	}
	if r.body != nil {
		call.RecordBody = recordBody(r.body)
	}
	resp, err := c.out.Do(ctx, call)
	if err != nil {
		return classify(resp, err)
	}
	if resp.Recovered {
		// Объект создан, но тело ответа потеряно: вызывающий найдёт его FindPayout.
		return fmt.Errorf("%w: %s recovered after timeout", ErrUnavailable, r.operation)
	}
	c.log.DebugContext(ctx, "rocketwork response", logx.KeyOp, "RocketWork."+r.operation, "status", resp.Status,
		"body", string(redactJSON(resp.Body)))
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%w: decode %s response: %v", ErrFormat, r.operation, err)
	}
	return nil
}

// classify переводит ошибку outbound в наши (05 §1, 05 §4.3): 404 — ErrNotFound; отказ 4xx
// с кодом — RejectedError; прочие 4xx — RejectedError provider_error; 5xx, 429, сеть —
// ErrUnavailable (повтор задания, outbound уже повторил внутри).
//
// ПРЕДПОЛОЖЕНИЕ (Q-10): 422 insufficient_funds, 409 contractor_not_active, 404 — объекта нет.
func classify(resp outbound.Response, err error) error {
	if _, ok := job.RetryAfter(err); ok {
		return err
	}
	if !job.IsPermanent(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.Status == http.StatusNotFound {
		return ErrNotFound
	}
	var body errorDTO
	_ = json.Unmarshal(resp.Body, &body)
	code := FailureProviderError
	if body.Error.Code != "" {
		code = FailureCodeFrom(body.Error.Code)
	}
	msg := body.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("status %d", resp.Status)
	}
	// ПРЕДПОЛОЖЕНИЕ (Q-10): 409 без делового кода — запрос с тем же ключом ещё выполняется; исход
	// неизвестен, а не отказ: задание ищет выплату по ключу и повторяет (R-6).
	if resp.Status == http.StatusConflict && code == FailureProviderError {
		return fmt.Errorf("%w: conflict: %s", ErrUnavailable, msg)
	}
	return &RejectedError{Code: code, Message: msg}
}

// EnsureContractor — POST /contractors; повтор с тем же ИНН возвращает существующего.
//
// ПРЕДПОЛОЖЕНИЕ (Q-10): метод и идемпотентность по ИНН.
func (c *Client) EnsureContractor(ctx context.Context, r ContractorRequest) (Contractor, error) {
	var d contractorDTO
	err := c.do(ctx, request{operation: OpEnsureContractor, method: http.MethodPost, path: "/contractors",
		objectID: r.ExternalID, key: "rw-contractor:" + r.ExternalID,
		body: contractorCreateDTO{ExternalID: r.ExternalID, INN: r.INN, Phone: r.Phone, FullName: r.FullName, LegalForm: r.LegalForm}}, &d)
	if err != nil {
		return Contractor{}, err
	}
	out := d.ours()
	c.log.InfoContext(ctx, "rocketwork contractor ensured", logx.KeyOp, "RocketWork.EnsureContractor",
		"external_id", out.ID, "status", out.Status)
	return out, nil
}

// GetContractor — GET /contractors/{id}.
func (c *Client) GetContractor(ctx context.Context, id string) (Contractor, error) {
	var d contractorDTO
	if err := c.do(ctx, request{operation: OpGetContractor, method: http.MethodGet,
		path: "/contractors/" + url.PathEscape(id), objectID: id}, &d); err != nil {
		return Contractor{}, err
	}
	return d.ours(), nil
}

// GetDepositBalance — GET /account/balance.
//
// ПРЕДПОЛОЖЕНИЕ (Q-10): {available_kopecks, currency}.
func (c *Client) GetDepositBalance(ctx context.Context) (int64, error) {
	var d struct {
		Available int64  `json:"available_kopecks"`
		Currency  string `json:"currency"`
	}
	if err := c.do(ctx, request{operation: OpGetDeposit, method: http.MethodGet, path: "/account/balance"}, &d); err != nil {
		return 0, err
	}
	if d.Currency != "" && d.Currency != "RUB" {
		return 0, fmt.Errorf("%w: deposit currency %s", ErrFormat, d.Currency)
	}
	return d.Available, nil
}

// CreatePayout — POST /payments с Idempotency-Key; при таймауте outbound проверяет выплату
// FindPayout (R-6).
func (c *Client) CreatePayout(ctx context.Context, r PaymentRequest) (Payment, error) {
	var d paymentDTO
	err := c.do(ctx, request{operation: OpCreatePayout, method: http.MethodPost, path: "/payments",
		key: r.IdempotencyKey, objectID: r.ExternalID, create: true,
		check: func(ctx context.Context) (bool, error) {
			_, found, err := c.FindPayout(ctx, r.IdempotencyKey)
			return found, err
		},
		body: paymentCreateDTO{ContractorID: r.ContractorID, AmountKopecks: r.AmountKopecks, Purpose: r.Purpose,
			ExternalID: r.ExternalID}}, &d)
	if err != nil {
		return Payment{}, err
	}
	return d.ours(), nil
}

// GetPayout — GET /payments/{id}.
func (c *Client) GetPayout(ctx context.Context, id string) (Payment, error) {
	var d paymentDTO
	if err := c.do(ctx, request{operation: OpGetPayout, method: http.MethodGet,
		path: "/payments/" + url.PathEscape(id), objectID: id}, &d); err != nil {
		return Payment{}, err
	}
	return d.ours(), nil
}

// FindPayout — GET /payments?external_id=… по нашему идентификатору попытки (ключ rw-payout:<id>).
//
// ПРЕДПОЛОЖЕНИЕ (Q-10): поиск по external_id, ответ {items: […]}.
func (c *Client) FindPayout(ctx context.Context, key string) (Payment, bool, error) {
	ext := strings.TrimPrefix(key, "rw-payout:")
	var d struct {
		Items []paymentDTO `json:"items"`
	}
	if err := c.do(ctx, request{operation: OpFindPayout, method: http.MethodGet, path: "/payments",
		query: url.Values{"external_id": {ext}}, objectID: ext}, &d); err != nil {
		return Payment{}, false, err
	}
	if len(d.Items) == 0 {
		return Payment{}, false, nil
	}
	return d.Items[0].ours(), true, nil
}

// ParseWebhook проверяет подпись (WebhookSecret) и разбирает тело.
func (c *Client) ParseWebhook(header map[string][]string, body []byte) (Event, error) {
	return ParseWebhook(c.opts.WebhookSecret, header, body)
}
