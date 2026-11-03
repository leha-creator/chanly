package rocketwork

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Disabled — режим ROCKETWORK_MODE=disabled (R-1): все вызовы возвращают ErrDisabled.
// WARN пишется не чаще раза в минуту на операцию.
type Disabled struct {
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewDisabled создаёт выключенный адаптер.
func NewDisabled(log *slog.Logger, now func() time.Time) *Disabled {
	if now == nil {
		now = time.Now
	}
	return &Disabled{log: log.With(logx.KeyComponent, "rocketwork"), now: now, last: map[string]time.Time{}}
}

func (d *Disabled) warn(ctx context.Context, op string) error {
	d.mu.Lock()
	now := d.now()
	quiet := now.Sub(d.last[op]) < time.Minute
	if !quiet {
		d.last[op] = now
	}
	d.mu.Unlock()
	if !quiet {
		d.log.WarnContext(ctx, "rocketwork disabled", logx.KeyOp, "RocketWork."+op)
	}
	return ErrDisabled
}

func (d *Disabled) EnsureContractor(ctx context.Context, _ ContractorRequest) (Contractor, error) {
	return Contractor{}, d.warn(ctx, "EnsureContractor")
}

func (d *Disabled) GetContractor(ctx context.Context, _ string) (Contractor, error) {
	return Contractor{}, d.warn(ctx, "GetContractor")
}

func (d *Disabled) GetDepositBalance(ctx context.Context) (int64, error) {
	return 0, d.warn(ctx, "GetDepositBalance")
}

func (d *Disabled) CreatePayout(ctx context.Context, _ PaymentRequest) (Payment, error) {
	return Payment{}, d.warn(ctx, "CreatePayout")
}

func (d *Disabled) GetPayout(ctx context.Context, _ string) (Payment, error) {
	return Payment{}, d.warn(ctx, "GetPayout")
}

func (d *Disabled) FindPayout(ctx context.Context, _ string) (Payment, bool, error) {
	return Payment{}, false, d.warn(ctx, "FindPayout")
}

func (d *Disabled) ParseWebhook(map[string][]string, []byte) (Event, error) {
	return Event{}, ErrDisabled
}
