package rocketwork

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// Public API v1: roketwork_openapi.json. Rate is a local limit, not a provider SLA.
const (
	DefaultBaseURL     = "https://api.rocketwork.ru"
	DefaultRate        = 5.0
	OpEnsureContractor = "EnsureContractor"
	OpGetContractor    = "GetContractor"
	OpGetDeposit       = "GetDepositBalance"
	OpCreatePayout     = "CreatePayout"
	OpGetPayout        = "GetPayout"
	OpFindPayout       = "FindPayout"
	busyRetry          = 30 * time.Second
)

type Options struct {
	Authorizer WriteAuthorizer
	BaseURL    string
	Token      string
	// Bearer credential configured in the postback subscription; HMAC is fake-only.
	WebhookSecret         []byte
	PreviousWebhookSecret []byte
	Rate                  float64
}

// NewHTTPClient keeps credentials on the configured origin and requires TLS 1.2.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := outbound.NewHTTPClient(transport)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

// Client implements Public API v1 reads and explicit writes. The application
// owns authorization, immutable terms, checkpoints and production admission.
type Client struct {
	out     *outbound.Client
	limiter *ratelimit.Limiter
	opts    Options
	log     *slog.Logger
}

func New(out *outbound.Client, limiter *ratelimit.Limiter, opts Options, log *slog.Logger) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if _, ok := opts.Authorizer.(emulatorAuthorization); ok {
		if _, err := LocalEmulatorAuthorization(opts.BaseURL); err != nil {
			opts.Authorizer = nil
		}
	}
	if opts.Rate <= 0 {
		opts.Rate = DefaultRate
	}
	return &Client{out: out, limiter: limiter, opts: opts, log: log.With(logx.KeyComponent, "rocketwork")}
}

func (c *Client) read(ctx context.Context, op, path string, query url.Values, result any) error {
	c.log.DebugContext(ctx, "rocketwork read started", logx.KeyOp, "RocketWork."+op)
	if err := c.limiter.Wait(ctx, ratelimit.Bucket{Key: "rocketwork:global", Rate: c.opts.Rate, Burst: c.opts.Rate}, ratelimit.PriorityPublish); err != nil {
		if errors.Is(err, ratelimit.ErrBusy) {
			return job.Retry(err, busyRetry)
		}
		return err
	}
	u := c.opts.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	resp, err := c.out.Do(ctx, outbound.Call{
		Operation: "RocketWork." + op, Method: http.MethodGet, URL: u,
		Header: http.Header{"Authorization": {"Bearer " + c.opts.Token}, "Accept": {"application/json"}, "Content-Type": {"application/json"}, "User-Agent": {"maxim-rocketwork/1"}},
		Redact: RedactFields, Sensitive: true,
	})
	if err != nil {
		c.log.WarnContext(ctx, "rocketwork read failed", logx.KeyOp, "RocketWork."+op, "status", resp.Status)
		if _, ok := job.RetryAfter(err); ok {
			return err
		}
		if resp.Status == http.StatusNotFound {
			return ErrNotFound
		}
		// Provider error text can contain requisites and personal data.
		if job.IsPermanent(err) {
			return &RejectedError{Code: FailureProviderError, Message: fmt.Sprintf("HTTP %d", resp.Status)}
		}
		return fmt.Errorf("%w: read failed", ErrUnavailable)
	}
	if err := json.Unmarshal(resp.Body, result); err != nil {
		c.log.WarnContext(ctx, "rocketwork invalid response", logx.KeyOp, "RocketWork."+op, "status", resp.Status)
		return fmt.Errorf("%w: invalid response", ErrFormat)
	}
	c.log.DebugContext(ctx, "rocketwork read completed", logx.KeyOp, "RocketWork."+op, "status", resp.Status)
	return nil
}

func (c *Client) blocked(ctx context.Context, op string) error {
	c.log.WarnContext(ctx, "rocketwork operation blocked", logx.KeyOp, "RocketWork."+op, "reason", "workflow_unconfigured")
	return fmt.Errorf("%w: %s requires a configured executor and agreement workflow", ErrDisabled, op)
}

// Registration needs passport data; invite sends SMS and does not return an
// onboarding URL. Neither can implement the existing port by guessing fields.
func (c *Client) EnsureContractor(ctx context.Context, _ ContractorRequest) (Contractor, error) {
	return Contractor{}, c.blocked(ctx, OpEnsureContractor)
}

// GET /api/executors/{id} accepts phone/INN/email, not the numeric executor ID
// stored by the old adapter. An explicit identity migration is required.
func (c *Client) GetContractor(ctx context.Context, _ string) (Contractor, error) {
	return Contractor{}, c.blocked(ctx, OpGetContractor)
}

func (c *Client) GetDepositBalance(ctx context.Context) (int64, error) {
	var d accountDTO
	if err := c.read(ctx, OpGetDeposit, "/api/account", nil, &d); err != nil {
		return 0, err
	}
	return rublesToKopecks(string(d.Balance))
}

// Creating a task is not a payout: the agreement, employment kind, acceptance
// and tax settings must be explicit. Never send the old /payments request.
func (c *Client) CreatePayout(ctx context.Context, _ PaymentRequest) (Payment, error) {
	return Payment{}, c.blocked(ctx, OpCreatePayout)
}

func (c *Client) GetPayout(ctx context.Context, id string) (Payment, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return Payment{}, fmt.Errorf("%w: invalid task id", ErrFormat)
	}
	var d struct {
		Task *taskDTO `json:"task"`
	}
	if err := c.read(ctx, OpGetPayout, "/api/tasks/"+id, nil, &d); err != nil {
		return Payment{}, err
	}
	if d.Task == nil || d.Task.ID != n {
		return Payment{}, fmt.Errorf("%w: task identity mismatch", ErrFormat)
	}
	return d.Task.payment()
}

func (c *Client) FindPayout(ctx context.Context, key string) (Payment, bool, error) {
	ext, ok := strings.CutPrefix(key, "rw-payout:")
	if !ok || strings.TrimSpace(ext) == "" {
		return Payment{}, false, ErrFormat
	}
	d, err := c.findTaskDTO(ctx, ext)
	if err != nil || d == nil {
		return Payment{}, false, err
	}
	p, err := d.payment()
	return p, err == nil, err
}

func (c *Client) FindTask(ctx context.Context, externalID string) (TaskSnapshot, bool, error) {
	if strings.TrimSpace(externalID) == "" {
		return TaskSnapshot{}, false, ErrFormat
	}
	d, err := c.findTaskDTO(ctx, externalID)
	if err != nil || d == nil {
		return TaskSnapshot{}, false, err
	}
	t, err := d.snapshot()
	return t, err == nil, err
}

func (c *Client) findTaskDTO(ctx context.Context, ext string) (*taskDTO, error) {
	var match *taskDTO
	total, last, received := -1, -1, 0
	for page := 1; page <= 1000; page++ {
		var d struct {
			Tasks *[]taskDTO `json:"tasks"`
			Meta  *pageDTO   `json:"meta"`
		}
		if err := c.read(ctx, OpFindPayout, "/api/tasks", url.Values{"external_ids[]": {ext}, "per_page": {"25"}, "page": {strconv.Itoa(page)}}, &d); err != nil {
			return nil, err
		}
		if d.Tasks == nil {
			return nil, fmt.Errorf("%w: missing tasks", ErrFormat)
		}
		if err := d.Meta.validate(page, len(*d.Tasks)); err != nil {
			return nil, err
		}
		if page == 1 {
			total, last = d.Meta.Total, d.Meta.Last
		}
		if d.Meta.Total != total || d.Meta.Last != last || (page < last && len(*d.Tasks) == 0) {
			return nil, fmt.Errorf("%w: changing task collection", ErrFormat)
		}
		for _, task := range *d.Tasks {
			if match != nil || task.ExternalID != ext {
				return nil, fmt.Errorf("%w: ambiguous task lookup", ErrFormat)
			}
			copy := task
			match = &copy
			received++
		}
		if page == last {
			if received != total {
				return nil, fmt.Errorf("%w: incomplete task collection", ErrFormat)
			}
			if match == nil {
				return nil, nil
			}
			return match, nil
		}
	}
	return nil, fmt.Errorf("%w: pagination limit", ErrFormat)
}

// Task callbacks only trigger an authoritative GET; payload status is not applied.
func (c *Client) ParseWebhook(header map[string][]string, body []byte) (Event, error) {
	return parsePostbackSecrets([][]byte{c.opts.WebhookSecret, c.opts.PreviousWebhookSecret}, header, body)
}
