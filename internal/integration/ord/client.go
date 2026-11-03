// Package ord — адаптер Яндекс ОРД, API v8 (05 §3, D-06). Вызывается только из обработчиков
// заданий маркировки вне транзакции (07 §1, I-13). Каждый запрос проходит общий ограничитель
// (корзина ord:global, R-20) и platform/outbound: повторы, предохранитель, запись в
// outbound_request с маскированным ИНН и без текста креатива (R-16).
//
// Все объекты идентифицируются нашими идентификаторами (R-1): POST — «регистрация или
// обновление» по id, поэтому повтор после неизвестного исхода безопасен и проверка
// существования не нужна.
package ord

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

// Адреса ОРД (05 §3.8).
const (
	DefaultBaseURL    = "https://ord.yandex.net"
	TestBaseURL       = "https://ord-prestable.yandex.net"
	DefaultAPIVersion = "v8"
	// DefaultRate — запросов в секунду на все экземпляры (Q-15, рабочее значение).
	DefaultRate = 5.0
)

// Операции адаптера: имена в outbound_request и в ordfake.
const (
	OpOrganization = "organization"
	OpContract     = "contract"
	OpPlatforms    = "platforms"
	OpCreative     = "creative"
	OpStatus       = "status"
	// Ежемесячная отчётность (этап «API администратора и отчётность ОРД», R-22).
	OpStatistics       = "statistics"
	OpInvoice          = "invoice"
	OpInvoiceCreatives = "invoice/creatives"
)

// busyRetry — через сколько повторить задание, если ограничитель занят.
const busyRetry = 30 * time.Second

// Options — подключение к ОРД.
type Options struct {
	BaseURL string
	// APIVersion — версия API в адресе (ORD_API_VERSION): все обращения собираются в этом пакете.
	APIVersion string
	// Token — OAuth-токен пользователя кабинета ОРД (секрет, 07 §4).
	Token string
	// Rate — ORD_RATE.
	Rate float64
}

// Client — адаптер Яндекс ОРД.
type Client struct {
	out     *outbound.Client
	limiter *ratelimit.Limiter
	opts    Options
	log     *slog.Logger
}

// New создаёт адаптер. out — клиент outbound системы yandex_ord.
func New(out *outbound.Client, limiter *ratelimit.Limiter, opts Options, log *slog.Logger) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.APIVersion == "" {
		opts.APIVersion = DefaultAPIVersion
	}
	if opts.Rate <= 0 {
		opts.Rate = DefaultRate
	}
	return &Client{out: out, limiter: limiter, opts: opts, log: log.With(logx.KeyComponent, "ord")}
}

// bucket — общий лимит ОРД (R-20): приоритетов нет, все вызовы фоновые.
func (c *Client) bucket() ratelimit.Bucket {
	return ratelimit.Bucket{Key: "ord:global", Rate: c.opts.Rate, Burst: c.opts.Rate}
}

// endpoint — адрес метода с версией API.
func (c *Client) endpoint(path string) string {
	return c.opts.BaseURL + "/api/" + c.opts.APIVersion + "/" + path
}

// request — запрос к ОРД.
type request struct {
	operation string
	method    string
	path      string
	query     url.Values
	body      any
	objectID  string
}

// do ждёт токен ограничителя, выполняет вызов и разбирает ResponseStatus.
func (c *Client) do(ctx context.Context, r request) (Receipt, error) {
	if err := c.limiter.Wait(ctx, c.bucket(), ratelimit.PriorityPublish); err != nil {
		if errors.Is(err, ratelimit.ErrBusy) {
			return Receipt{}, job.Retry(err, busyRetry)
		}
		return Receipt{}, err
	}
	u := c.endpoint(r.path)
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	call := outbound.Call{
		Operation:  "ORD." + r.operation,
		Method:     r.method,
		URL:        u,
		Header:     http.Header{"Authorization": {"Bearer " + c.opts.Token}},
		Body:       r.body,
		ObjectType: r.operation,
		ObjectID:   r.objectID,
	}
	if r.body != nil {
		call.RecordBody = recordBody(r.body)
	}
	resp, err := c.out.Do(ctx, call)
	if err != nil {
		return Receipt{}, classify(resp, err)
	}
	var rec Receipt
	if err := json.Unmarshal(resp.Body, &rec); err != nil {
		return Receipt{}, fmt.Errorf("ord %s: decode response: %w", r.operation, err)
	}
	return rec, nil
}

// submit — POST объекта: журнал «внешняя операция выполнена» (07 §5.3).
func (c *Client) submit(ctx context.Context, operation, objectID string, body any) (Receipt, error) {
	rec, err := c.do(ctx, request{operation: operation, method: http.MethodPost, path: operation, body: body, objectID: objectID})
	if err != nil {
		return Receipt{}, err
	}
	c.log.InfoContext(ctx, "ord object submitted", logx.KeyOp, "ORD.Submit", "ord_object_type", operation,
		"ord_object_id", objectID, "reqid", rec.RequestID, "ord_status", rec.Status)
	return rec, nil
}
