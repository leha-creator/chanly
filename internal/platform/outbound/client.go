// Package outbound — обёртка внешних вызовов по 05 §1: таймауты, повторы,
// разбор 429 и 4xx, неизвестный исход создания, предохранитель по системе и
// запись каждой попытки в outbound_request без секретов.
//
// Вызывается только адаптерами integration/* из обработчиков заданий (07 §1).
// Ошибки возвращаются в терминах планировщика: job.Permanent — повтор не
// поможет, job.Retry — отложить задание.
package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound/sqlc"
)

// Системы (04 §8, outbound_request.system).
const (
	SystemMAX        = "max"
	SystemYandexORD  = "yandex_ord"
	SystemRocketWork = "rocketwork"
)

// Параметры 05 §1.
const (
	ConnectTimeout = 5 * time.Second
	RequestTimeout = 30 * time.Second
	UploadTimeout  = 120 * time.Second
	// maxInlineRetryAfter — дольше этого Retry-After не ждём внутри задания, а
	// откладываем задание (job.Retry).
	maxInlineRetryAfter = 30 * time.Second
	maxResponseBody     = 10 << 20
	errorTextLimit      = 512
)

// retryDelays — задержки перед повторами внутри одного вызова: первая попытка
// и до трёх повторов через 1, 2, 4 с (05 §1). Дальше — повтор задания.
var retryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Alerter поднимает оповещение администратору (07 §3). Реализация — platform/alert.
type Alerter interface {
	RaiseError(ctx context.Context, reason, objectKey, summary string, fields map[string]any)
}

type nopAlerter struct{}

func (nopAlerter) RaiseError(context.Context, string, string, string, map[string]any) {}

// Options — настройки клиента одной внешней системы.
type Options struct {
	System string
	// HTTP — клиент запросов; по умолчанию с таймаутом соединения 5 с.
	// Таймаут запроса задаётся context каждой попытки.
	HTTP *http.Client
	// NoRetry disables inline retries for diagnostic runs. Timeout recovery via
	// CheckExisting still runs; an unconfirmed outcome never causes a second request.
	NoRetry bool
	// Redact — дополнительные поля и параметры, скрываемые в outbound_request.
	Redact []string
	// Sleep ждёт между попытками; в тестах подменяется.
	Sleep   func(ctx context.Context, d time.Duration) error
	Alerter Alerter
	Metrics *Metrics
}

// Client выполняет вызовы одной внешней системы.
type Client struct {
	pool     *pgxpool.Pool
	clock    clock.Clock
	log      *slog.Logger
	opts     Options
	redactor redactor
	circuit  circuit
}

// NewHTTPClient — клиент с таймаутом соединения 5 с (05 §1).
func NewHTTPClient(base *http.Transport) *http.Client {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	base.DialContext = (&net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	base.TLSHandshakeTimeout = ConnectTimeout
	return &http.Client{Transport: base}
}

// New создаёт клиент системы opts.System.
func New(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger, opts Options) *Client {
	if opts.HTTP == nil {
		opts.HTTP = NewHTTPClient(nil)
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	if opts.Alerter == nil {
		opts.Alerter = nopAlerter{}
	}
	c := &Client{pool: pool, clock: clk, log: log, opts: opts, redactor: newRedactor(opts.Redact)}
	c.circuit = circuit{pool: pool, clock: clk, log: log, system: opts.System, alerter: opts.Alerter, metrics: opts.Metrics}
	return c
}

// System — имя системы клиента.
func (c *Client) System() string { return c.opts.System }

// Call — один вызов внешней системы.
type Call struct {
	// Operation — имя операции адаптера: GetMe, SendMessage…
	Operation string
	Method    string
	URL       string
	Header    http.Header
	// Body сериализуется в JSON; RawBody — тело как есть (с ContentType).
	Body        any
	RawBody     []byte
	ContentType string
	// IdempotencyKey передаётся всегда, если задан (05 §1), в заголовке IdempotencyHeader.
	IdempotencyKey    string
	IdempotencyHeader string
	ObjectType        string
	ObjectID          string
	// Create — вызов создаёт объект: при таймауте перед повтором вызывается
	// CheckExisting (03 §11, «неизвестный исход»).
	Create        bool
	CheckExisting func(ctx context.Context) (bool, error)
	// Upload — загрузка файла, таймаут 120 с.
	Upload bool
	// ValidateBody превращает 2xx с отказом в теле в ошибку (MAX success=false).
	ValidateBody func(Response) error
	// Redact — поля этого вызова, скрываемые в outbound_request сверх Options.Redact
	// (адрес подписки Webhook с секретной частью пути).
	Redact []string
	// RecordBody — тело запроса для outbound_request вместо отправленного: адаптер сам
	// маскирует поля, которые нельзя просто скрыть (ИНН маскированным, длина текста).
	// Поля Redact к нему тоже применяются.
	RecordBody []byte
	// ExpectedRejections — коды отказа, которые для вызова — ожидаемый ответ, а не сбой: без
	// повторов, WARN вместо ERROR и без оповещения (бот не может писать пользователю, Q-38).
	ExpectedRejections []int
}

// Response — ответ внешней системы.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// Recovered — запрос завершился таймаутом, но объект найден CheckExisting.
	Recovered bool
}

// RejectedError — внешняя система отказала (4xx кроме 429 или отказ в теле).
// Повтор не поможет: объект переводится в состояние ошибки (05 §1).
type RejectedError struct {
	System    string
	Operation string
	Status    int
	// Body — тело ответа после маскирования, обрезанное.
	Body   string
	Reason string
}

func (e *RejectedError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s %s rejected: %s", e.System, e.Operation, e.Reason)
	}
	return fmt.Sprintf("%s %s rejected with status %d: %s", e.System, e.Operation, e.Status, e.Body)
}

// ErrCircuitOpen — вызовы системы приостановлены предохранителем.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// attemptResult — итог одной попытки.
type attemptResult struct {
	resp      Response
	err       error
	retryable bool
	// systemFailure — сбой системы (сеть, таймаут, 5xx): считается предохранителем.
	systemFailure bool
	timeout       bool
	retryAfter    time.Duration
}

// Do выполняет вызов с повторами по 05 §1.
func (c *Client) Do(ctx context.Context, call Call) (Response, error) {
	log := c.log.With(logx.KeyOp, "Outbound."+call.Operation, "system", c.opts.System, "operation", call.Operation)
	if wait, open, err := c.circuit.check(ctx); err != nil {
		return Response{}, err
	} else if open {
		log.WarnContext(ctx, "outbound call skipped, circuit open", "retry_in", wait.String())
		c.opts.Metrics.observe(c.opts.System, call.Operation, outcomeCircuitOpen, 0)
		return Response{}, job.Retry(fmt.Errorf("%s: %w", c.opts.System, ErrCircuitOpen), wait)
	}

	var last attemptResult
	for attempt := 1; attempt <= len(retryDelays)+1; attempt++ {
		last = c.attempt(ctx, log, call, attempt)
		if last.err == nil {
			c.circuit.success(ctx)
			return last.resp, nil
		}
		if last.timeout && call.Create && call.CheckExisting != nil {
			found, cerr := call.CheckExisting(ctx)
			if cerr == nil && found {
				log.InfoContext(ctx, "outbound create recovered after timeout", "attempt", attempt)
				c.circuit.success(ctx)
				return Response{Recovered: true}, nil
			}
			if cerr != nil {
				log.WarnContext(ctx, "outbound existence check failed", "attempt", attempt, logx.KeyError, cerr.Error())
			}
		}
		if last.systemFailure {
			if wait, opened := c.circuit.failure(ctx); opened {
				return Response{}, job.Retry(fmt.Errorf("%s %s: %w: %v", c.opts.System, call.Operation, ErrCircuitOpen, last.err), wait)
			}
		} else if !last.retryable {
			c.circuit.success(ctx) // система ответила по существу — она работает
		}
		if !last.retryable && slices.Contains(call.ExpectedRejections, last.resp.Status) {
			log.WarnContext(ctx, "outbound call rejected as expected", "attempt", attempt, "status", last.resp.Status,
				logx.KeyError, last.err.Error())
			return last.resp, job.Permanent(last.err)
		}
		if !last.retryable {
			log.ErrorContext(ctx, "outbound call rejected", "attempt", attempt, "status", last.resp.Status,
				"response", c.redactor.text(last.resp.Body, errorTextLimit), logx.KeyError, last.err.Error())
			c.alert(ctx, call, last.err)
			return last.resp, job.Permanent(last.err)
		}
		if c.opts.NoRetry {
			log.WarnContext(ctx, "outbound inline retry disabled", "attempt", attempt, "status", last.resp.Status)
			return last.resp, last.err
		}
		if attempt > len(retryDelays) {
			break
		}
		delay := retryDelays[attempt-1]
		if last.retryAfter > 0 {
			if last.retryAfter > maxInlineRetryAfter {
				log.WarnContext(ctx, "outbound call throttled, job deferred", "attempt", attempt, "retry_after", last.retryAfter.String())
				return last.resp, job.Retry(last.err, last.retryAfter)
			}
			delay = last.retryAfter
		}
		log.WarnContext(ctx, "outbound call failed, will retry", "attempt", attempt, "status", last.resp.Status,
			"retry_in", delay.String(), logx.KeyError, last.err.Error())
		if err := c.opts.Sleep(ctx, delay); err != nil {
			return Response{}, err
		}
	}
	log.ErrorContext(ctx, "outbound retries exhausted", "attempts", len(retryDelays)+1, "status", last.resp.Status,
		"response", c.redactor.text(last.resp.Body, errorTextLimit), logx.KeyError, last.err.Error())
	c.alert(ctx, call, last.err)
	return last.resp, fmt.Errorf("%s %s: retries exhausted: %w", c.opts.System, call.Operation, last.err)
}

func (c *Client) alert(ctx context.Context, call Call, err error) {
	c.opts.Alerter.RaiseError(ctx, "outbound_failed:"+c.opts.System+":"+call.Operation, call.ObjectID,
		fmt.Sprintf("%s %s failed", c.opts.System, call.Operation),
		map[string]any{"object_type": call.ObjectType, "object_id": call.ObjectID, "error": truncate(err.Error(), errorTextLimit)})
}

// attempt выполняет одну попытку и записывает её в outbound_request.
func (c *Client) attempt(ctx context.Context, log *slog.Logger, call Call, attempt int) attemptResult {
	timeout := RequestTimeout
	if call.Upload {
		timeout = UploadTimeout
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, body, err := c.build(actx, call)
	if err != nil {
		return attemptResult{err: fmt.Errorf("build request: %w", err)}
	}
	log.DebugContext(ctx, "outbound call started", "attempt", attempt, "method", call.Method, "url", c.redactor.url(call.URL))
	started := c.clock.Now()
	res := c.send(req, call)
	dur := c.clock.Now().Sub(started)
	if res.err != nil && ctx.Err() != nil { // отменён context задания: не повторять
		res.retryable, res.systemFailure = false, false
		res.err = ctx.Err()
	}
	log.DebugContext(ctx, "outbound call finished", "attempt", attempt, "status", res.resp.Status,
		"duration_ms", dur.Milliseconds(), "ok", res.err == nil)
	c.record(ctx, call, attempt, req, body, res, started, dur)
	c.opts.Metrics.observe(c.opts.System, call.Operation, outcomeOf(res), dur)
	return res
}

func (c *Client) build(ctx context.Context, call Call) (*http.Request, []byte, error) {
	body := call.RawBody
	contentType := call.ContentType
	if call.Body != nil {
		b, err := json.Marshal(call.Body)
		if err != nil {
			return nil, nil, err
		}
		body, contentType = b, "application/json"
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, call.Method, call.URL, reader)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range call.Header {
		req.Header[k] = append([]string(nil), v...)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if call.IdempotencyKey != "" {
		h := call.IdempotencyHeader
		if h == "" {
			h = "Idempotency-Key"
		}
		req.Header.Set(h, call.IdempotencyKey)
	}
	return req, body, nil
}

func (c *Client) send(req *http.Request, call Call) attemptResult {
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		var ne net.Error
		timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
		return attemptResult{err: err, retryable: true, systemFailure: true, timeout: timeout}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	r := Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
	if err != nil {
		return attemptResult{resp: r, err: fmt.Errorf("read response: %w", err), retryable: true, systemFailure: true}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return attemptResult{resp: r, err: fmt.Errorf("status %d", resp.StatusCode), retryable: true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), c.clock.Now())}
	case resp.StatusCode >= 500:
		return attemptResult{resp: r, err: fmt.Errorf("status %d", resp.StatusCode), retryable: true, systemFailure: true}
	case resp.StatusCode >= 400:
		return attemptResult{resp: r, err: &RejectedError{System: c.opts.System, Operation: call.Operation,
			Status: resp.StatusCode, Body: c.redactor.text(body, errorTextLimit)}}
	case resp.StatusCode >= 300:
		return attemptResult{resp: r, err: &RejectedError{System: c.opts.System, Operation: call.Operation,
			Status: resp.StatusCode, Reason: "unexpected redirect"}}
	}
	if call.ValidateBody != nil {
		if err := call.ValidateBody(r); err != nil {
			return attemptResult{resp: r, err: &RejectedError{System: c.opts.System, Operation: call.Operation,
				Status: resp.StatusCode, Body: c.redactor.text(body, errorTextLimit), Reason: err.Error()}}
		}
	}
	return attemptResult{resp: r}
}

// record пишет попытку в outbound_request вне транзакции вызывающего: запись
// остаётся и при откате.
func (c *Client) record(ctx context.Context, call Call, attempt int, req *http.Request, body []byte,
	res attemptResult, started time.Time, dur time.Duration) {
	red := c.redactor.with(call.Redact)
	if call.RecordBody != nil {
		body = call.RecordBody
	}
	request, _ := json.Marshal(map[string]any{
		"method":  call.Method,
		"url":     red.url(call.URL),
		"headers": red.headers(req.Header),
		"body":    red.body(body),
	})
	p := sqlc.InsertOutboundRequestParams{
		ID: ids.New(), System: c.opts.System, Operation: call.Operation, Attempt: int32(attempt),
		Request: request, CreatedAt: started, ResponseBody: red.bodyJSON(res.resp.Body),
	}
	ms := int32(dur.Milliseconds())
	p.DurationMs = &ms
	if res.resp.Status != 0 {
		status := int32(res.resp.Status)
		p.ResponseStatus = &status
	}
	if res.err != nil {
		msg := truncate(res.err.Error(), errorTextLimit)
		p.Error = &msg
	}
	if call.IdempotencyKey != "" {
		p.IdempotencyKey = &call.IdempotencyKey
	}
	if call.ObjectType != "" {
		p.ObjectType = &call.ObjectType
	}
	if call.ObjectID != "" {
		p.ObjectID = &call.ObjectID
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := sqlc.New(c.pool).InsertOutboundRequest(rctx, p); err != nil {
		c.log.ErrorContext(ctx, "outbound request record failed", logx.KeyOp, "Outbound.Record",
			"system", c.opts.System, "operation", call.Operation, logx.KeyError, err.Error())
	}
}

// parseRetryAfter понимает секунды и дату HTTP.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
