// Package max — адаптер MAX Bot API (05 §2). Вызывается только из обработчиков
// заданий (07 §1). Каждый запрос проходит общий ограничитель (05 §2.8) и
// platform/outbound: повторы, предохранитель, запись в outbound_request.
//
// В этапе «Планировщик заданий и интеграционный слой» — транспорт, ошибки и
// отправка сообщений; методы каналов, публикации и загрузки добавляют следующие этапы.
package max

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// DefaultBaseURL — адрес MAX Bot API (05 §2.1).
const DefaultBaseURL = "https://platform-api2.max.ru"

// MaxTextLength — предел текста сообщения (05 §2.3).
const MaxTextLength = 4000

// busyRetry — через сколько повторить задание, если ограничитель занят.
const busyRetry = 30 * time.Second

// Options — подключение к MAX.
type Options struct {
	BaseURL string
	Token   string
	// GlobalRate — запросов в секунду на все экземпляры (05 §2.8: 25 из 30).
	GlobalRate float64
	// ChatRate — сообщений в секунду в один чат (05 §2.8: 1 из 2).
	ChatRate float64
	// BotUserID — пользователь MAX бота (MAX_BOT_USER_ID); 0 — узнать через GET /me (R-8).
	BotUserID int64
	// Sleep — ожидание между повторами attachment.not.ready; по умолчанию — таймер.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client — адаптер MAX Bot API.
type Client struct {
	out     *outbound.Client
	limiter *ratelimit.Limiter
	opts    Options
	log     *slog.Logger

	botMu     sync.Mutex
	botUserID int64
}

// NewHTTPClient — HTTP-клиент с системными корневыми сертификатами и, если
// задан, корневым сертификатом Минцифры из caFile (05 §2.1).
func NewHTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read MAX CA certificate: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("MAX CA certificate: no PEM certificates found")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return outbound.NewHTTPClient(transport), nil
}

// New создаёт адаптер. out — клиент outbound системы max.
func New(out *outbound.Client, limiter *ratelimit.Limiter, opts Options, log *slog.Logger) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.GlobalRate <= 0 {
		opts.GlobalRate = 25
	}
	if opts.ChatRate <= 0 {
		opts.ChatRate = 1
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	return &Client{out: out, limiter: limiter, opts: opts, log: log, botUserID: opts.BotUserID}
}

type priorityKey struct{}

// WithPriority задаёт приоритет запросов MAX для ограничителя (05 §2.8).
// По умолчанию — ответы и уведомления.
func WithPriority(ctx context.Context, p ratelimit.Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, p)
}

func priority(ctx context.Context) ratelimit.Priority {
	if p, ok := ctx.Value(priorityKey{}).(ratelimit.Priority); ok {
		return p
	}
	return ratelimit.PriorityReply
}

// globalBucket — общий лимит платформы.
func (c *Client) globalBucket() ratelimit.Bucket {
	return ratelimit.Bucket{Key: "max:global", Rate: c.opts.GlobalRate, Burst: c.opts.GlobalRate}
}

// chatBucket — лимит сообщений в один чат или пользователю.
func (c *Client) chatBucket(kind string, id int64) ratelimit.Bucket {
	return ratelimit.Bucket{Key: "max:" + kind + ":" + strconv.FormatInt(id, 10), Rate: c.opts.ChatRate, Burst: 1}
}

// request — запрос к MAX: ожидание токенов, затем outbound.
type request struct {
	operation string
	method    string
	path      string
	query     url.Values
	body      any
	buckets   []ratelimit.Bucket
	object    string
	objectID  string
	redact    []string
	// create и checkExisting — вызов создаёт объект: при таймауте перед повтором проверить,
	// не создан ли он (03 §11).
	create        bool
	checkExisting func(ctx context.Context) (bool, error)
	// expected — коды отказа, ожидаемые для вызова (outbound.Call.ExpectedRejections).
	expected []int
}

func (c *Client) do(ctx context.Context, r request) (outbound.Response, error) {
	p := priority(ctx)
	for _, b := range append([]ratelimit.Bucket{c.globalBucket()}, r.buckets...) {
		if err := c.limiter.Wait(ctx, b, p); err != nil {
			if errors.Is(err, ratelimit.ErrBusy) {
				return outbound.Response{}, job.Retry(err, busyRetry)
			}
			return outbound.Response{}, err
		}
	}
	u := c.opts.BaseURL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	return c.out.Do(ctx, outbound.Call{
		Operation:          r.operation,
		Method:             r.method,
		URL:                u,
		Header:             http.Header{"Authorization": {c.opts.Token}},
		Body:               r.body,
		ObjectType:         r.object,
		ObjectID:           r.objectID,
		ValidateBody:       validateSuccess,
		Redact:             r.redact,
		Create:             r.create,
		CheckExisting:      r.checkExisting,
		ExpectedRejections: r.expected,
	})
}

// sleep ждёт d или отмены context.
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
