package rocketwork

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
)

type ExecutorIdentity struct {
	Kind, Value string
	// These independent expectations prevent a changed phone/INN from silently
	// rebinding the organization to another executor.
	ExpectedID                             int64
	INN, Phone, ExternalID, EmploymentKind string
}

type Executor struct {
	ID                                            int64
	INN, Phone, Email, ExternalID, EmploymentKind string
	Status, Reason, NextAction                    string
	Known                                         bool
}

func (r ExecutorIdentity) validate() error {
	if len(r.Value) > 320 || strings.TrimSpace(r.Value) != r.Value {
		return fmt.Errorf("%w: executor lookup", ErrFormat)
	}
	digits := func(s string) bool {
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return s != ""
	}
	switch r.Kind {
	case "inn":
		if len(r.Value) != 12 || !digits(r.Value) {
			return fmt.Errorf("%w: executor INN", ErrFormat)
		}
	case "phone":
		if len(r.Value) < 11 || len(r.Value) > 16 || r.Value[0] != '+' || !digits(r.Value[1:]) {
			return fmt.Errorf("%w: executor phone", ErrFormat)
		}
	case "email":
		a, err := mail.ParseAddress(r.Value)
		if err != nil || a.Address != r.Value {
			return fmt.Errorf("%w: executor email", ErrFormat)
		}
	default:
		return fmt.Errorf("%w: executor lookup kind", ErrFormat)
	}
	if r.EmploymentKind != "entrepreneur" && r.EmploymentKind != "selfemployed" {
		return fmt.Errorf("%w: employment kind", ErrFormat)
	}
	return nil
}

func (d executorDTO) matches(r ExecutorIdentity) bool {
	value := ""
	switch r.Kind {
	case "inn":
		value = d.INN
	case "phone":
		value = d.Phone
	case "email":
		value = d.Email
	}
	return d.ID > 0 && value == r.Value && (r.ExpectedID == 0 || r.ExpectedID == d.ID) &&
		(r.INN == "" || r.INN == d.INN) && (r.Phone == "" || r.Phone == d.Phone) &&
		(r.ExternalID == "" || d.ExternalID == "" || r.ExternalID == d.ExternalID)
}

func (d executorDTO) executor(kind string) Executor {
	e := Executor{ID: d.ID, INN: d.INN, Phone: d.Phone, Email: d.Email, ExternalID: d.ExternalID, EmploymentKind: d.EmploymentKind, Status: ContractorPending, Reason: "unknown_readiness", NextAction: "reconcile"}
	if d.EmploymentKind != kind {
		e.Reason = "employment_mismatch"
		return e
	}
	switch d.PaymentReadiness {
	case "all_is_well":
	case "no_tax_payment":
		e.Status, e.Reason, e.NextAction, e.Known = ContractorBlocked, "need_permissions", "grant_fns_permissions", true
		return e
	case "no_requisites":
		e.Status, e.Reason, e.NextAction, e.Known = ContractorBlocked, "no_requisites", "add_requisites", true
		return e
	case "no_payments":
		e.Reason, e.NextAction, e.Known = "incomplete_profile", "complete_registration", true
		return e
	default:
		return e
	}
	// Profiles identify information sources and are optional in the Public API.
	// Use the provider's readiness and self-employment summary for admission.
	if kind == "selfemployed" && d.SelfemployedStatus != "validated" {
		e.Reason, e.NextAction = "selfemployment_unverified", "grant_fns_permissions"
		return e
	}
	e.Status, e.Reason, e.NextAction, e.Known = ContractorActive, "", "", true
	return e
}

func (c *Client) LookupExecutor(ctx context.Context, r ExecutorIdentity) (Executor, error) {
	if err := r.validate(); err != nil {
		return Executor{}, err
	}
	var response struct {
		Executor *executorDTO `json:"executor"`
	}
	if err := c.read(ctx, "LookupExecutor", "/api/executors/"+url.PathEscape(r.Value), nil, &response); err != nil {
		return Executor{}, err
	}
	if response.Executor == nil || !response.Executor.matches(r) {
		c.log.WarnContext(ctx, "rocketwork executor identity mismatch")
		return Executor{}, fmt.Errorf("%w: executor identity mismatch", ErrFormat)
	}
	e := response.Executor.executor(r.EmploymentKind)
	c.log.DebugContext(ctx, "[FIX] rocketwork executor readiness read", "status", e.Status, "reason", e.Reason,
		"payment_readiness", response.Executor.PaymentReadiness, "selfemployed_status", response.Executor.SelfemployedStatus)
	return e, nil
}

// FindExecutor treats search as fuzzy and checks all pages for exact matches.
// A conflicting match is an error, never permission to register a replacement.
func (c *Client) FindExecutor(ctx context.Context, r ExecutorIdentity) (Executor, bool, error) {
	if err := r.validate(); err != nil {
		return Executor{}, false, err
	}
	var match *executorDTO
	total, last, received := -1, -1, 0
	seen := map[int64]bool{}
	for page := 1; page <= 1000; page++ {
		var response struct {
			Executors *[]executorDTO `json:"executors"`
			Meta      *pageDTO       `json:"meta"`
		}
		if err := c.read(ctx, "FindExecutor", "/api/executors", url.Values{"search": {r.Value}, "page": {strconv.Itoa(page)}, "per_page": {"25"}}, &response); err != nil {
			return Executor{}, false, err
		}
		if response.Executors == nil {
			return Executor{}, false, fmt.Errorf("%w: missing executors", ErrFormat)
		}
		if err := response.Meta.validate(page, len(*response.Executors)); err != nil {
			return Executor{}, false, err
		}
		if page == 1 {
			total, last = response.Meta.Total, response.Meta.Last
		}
		if total != response.Meta.Total || last != response.Meta.Last || (page < last && len(*response.Executors) == 0) {
			return Executor{}, false, fmt.Errorf("%w: changing executor collection", ErrFormat)
		}
		for _, d := range *response.Executors {
			if d.ID <= 0 || seen[d.ID] {
				return Executor{}, false, fmt.Errorf("%w: duplicate executor collection", ErrFormat)
			}
			seen[d.ID] = true
			received++
			exact := d.INN == r.Value || d.Phone == r.Value || d.Email == r.Value
			if !exact {
				continue
			}
			if match != nil || !d.matches(r) {
				return Executor{}, false, fmt.Errorf("%w: conflicting executor", ErrFormat)
			}
			copy := d
			match = &copy
		}
		if page == last {
			if received != total {
				return Executor{}, false, fmt.Errorf("%w: incomplete executor collection", ErrFormat)
			}
			if match == nil {
				return Executor{}, false, nil
			}
			e := match.executor(r.EmploymentKind)
			c.log.DebugContext(ctx, "[FIX] rocketwork executor readiness read", "status", e.Status, "reason", e.Reason,
				"payment_readiness", match.PaymentReadiness, "selfemployed_status", match.SelfemployedStatus)
			return e, true, nil
		}
	}
	return Executor{}, false, fmt.Errorf("%w: executor pagination limit", ErrFormat)
}
