package rocketwork

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type EntrepreneurRequest struct {
	INN        string `json:"inn"`
	Phone      string `json:"phone"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	SecondName string `json:"second_name,omitempty"`
}

func (r EntrepreneurRequest) validate() error {
	if err := (ExecutorIdentity{Kind: "inn", Value: r.INN, EmploymentKind: "entrepreneur"}).validate(); err != nil {
		return err
	}
	if err := (ExecutorIdentity{Kind: "phone", Value: r.Phone, EmploymentKind: "entrepreneur"}).validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.FirstName) == "" || strings.TrimSpace(r.LastName) == "" {
		return fmt.Errorf("%w: explicit first and last name required", ErrFormat)
	}
	return nil
}

// RegisterEntrepreneur never updates an existing identity as a side effect.
func (c *Client) RegisterEntrepreneur(ctx context.Context, r EntrepreneurRequest) (Executor, error) {
	if err := r.validate(); err != nil {
		return Executor{}, err
	}
	e, err := c.LookupExecutor(ctx, ExecutorIdentity{Kind: "inn", Value: r.INN, Phone: r.Phone, EmploymentKind: "entrepreneur"})
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Executor{}, err
	}
	return c.saveEntrepreneur(ctx, r, 0)
}

func (c *Client) UpdateEntrepreneur(ctx context.Context, r EntrepreneurRequest, expectedID int64) (Executor, error) {
	if err := r.validate(); err != nil {
		return Executor{}, err
	}
	if expectedID <= 0 {
		return Executor{}, fmt.Errorf("%w: verified executor required", ErrFormat)
	}
	if _, err := c.LookupExecutor(ctx, ExecutorIdentity{Kind: "inn", Value: r.INN, Phone: r.Phone, EmploymentKind: "entrepreneur", ExpectedID: expectedID}); err != nil {
		return Executor{}, err
	}
	return c.saveEntrepreneur(ctx, r, expectedID)
}

func (c *Client) saveEntrepreneur(ctx context.Context, r EntrepreneurRequest, expectedID int64) (Executor, error) {
	payload := struct {
		Executor any `json:"executor"`
	}{Executor: struct {
		EntrepreneurRequest
		EmploymentKind string `json:"employment_kind"`
	}{r, "entrepreneur"}}
	var response struct {
		Executor *executorDTO `json:"executor"`
	}
	if err := c.write(ctx, "SaveEntrepreneur", http.MethodPost, "/api/executors", payload, &response); err != nil {
		return Executor{}, err
	}
	identity := ExecutorIdentity{Kind: "inn", Value: r.INN, Phone: r.Phone, ExpectedID: expectedID, EmploymentKind: "entrepreneur"}
	if response.Executor == nil || !response.Executor.matches(identity) {
		return Executor{}, ErrUncertain
	}
	return response.Executor.executor("entrepreneur"), nil
}

type InvitationRequest struct {
	Phone                  string `json:"phone"`
	WithFrameworkAgreement bool   `json:"with_framework_agreement"`
}

func (c *Client) InviteExecutor(ctx context.Context, r InvitationRequest) (string, error) {
	if err := (ExecutorIdentity{Kind: "phone", Value: r.Phone, EmploymentKind: "selfemployed"}).validate(); err != nil {
		return "", err
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := c.write(ctx, "InviteExecutor", http.MethodPost, "/api/executors/invite", r, &response); err != nil {
		return "", err
	}
	switch response.Status {
	case "invitation_sent", "invitation_exists", "executor_exists":
		return response.Status, nil
	}
	return "", ErrUncertain
}

func (c *Client) RebindExecutor(ctx context.Context, r ExecutorIdentity) (Executor, error) {
	if err := r.validate(); err != nil {
		return Executor{}, err
	}
	if r.Kind != "inn" || r.ExpectedID <= 0 || r.EmploymentKind != "selfemployed" {
		return Executor{}, fmt.Errorf("%w: rebind requires verified selfemployed INN", ErrFormat)
	}
	e, err := c.LookupExecutor(ctx, r)
	if err != nil {
		return Executor{}, err
	}
	if e.NextAction != "grant_fns_permissions" {
		return Executor{}, fmt.Errorf("%w: FNS permission request not required", ErrFormat)
	}
	var response struct {
		Executor *executorDTO `json:"executor"`
	}
	if err := c.write(ctx, "RebindExecutor", http.MethodPost, "/api/executors/rebind", struct {
		INN string `json:"inn"`
	}{r.Value}, &response); err != nil {
		return Executor{}, err
	}
	if response.Executor == nil || !response.Executor.matches(r) {
		return Executor{}, ErrUncertain
	}
	return response.Executor.executor(r.EmploymentKind), nil
}
