package rocketwork

import "context"

// Validate checks an immutable diagnostic plan without network access.
func (t TaskTerms) Validate() error           { return t.validate() }
func (r ExecutorIdentity) Validate() error    { return r.validate() }
func (r EntrepreneurRequest) Validate() error { return r.validate() }
func (r SubscriptionRequest) Validate() error {
	if !r.valid() {
		return ErrFormat
	}
	return nil
}

type Account struct {
	CompanyINN     string
	BalanceKopecks int64
}

func (c *Client) Account(ctx context.Context) (Account, error) {
	var d accountDTO
	if err := c.read(ctx, "InspectAccount", "/api/account", nil, &d); err != nil {
		return Account{}, err
	}
	b, err := rublesToKopecks(string(d.Balance))
	if err != nil || (!digits(d.CompanyINN, 10) && !digits(d.CompanyINN, 12)) {
		return Account{}, ErrFormat
	}
	return Account{CompanyINN: d.CompanyINN, BalanceKopecks: b}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	var response struct {
		Message string `json:"message"`
	}
	if err := c.read(ctx, "Ping", "/api/ping", nil, &response); err != nil {
		return err
	}
	if response.Message == "" {
		return ErrFormat
	}
	return nil
}
