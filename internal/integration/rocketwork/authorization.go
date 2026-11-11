package rocketwork

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
)

var ErrPolicyDenied = errors.New("rocketwork: write policy denied")

type WriteRequest struct {
	Operation, Method, Path string
	Body                    json.RawMessage
	Before                  *TaskSnapshot
}
type WriteAuthorizer interface {
	AuthorizeWrite(context.Context, WriteRequest) error
}

// AutomationAuthorization is composed only by the admitted production runtime.
// Local test harnesses have a distinct origin-restricted authorization below.
type AutomationAuthorization struct{}

func (AutomationAuthorization) AuthorizeWrite(context.Context, WriteRequest) error { return nil }

type emulatorAuthorization struct{}

func (emulatorAuthorization) AuthorizeWrite(context.Context, WriteRequest) error { return nil }

// LocalEmulatorAuthorization never grants authority for a production origin.
func LocalEmulatorAuthorization(origin string) (WriteAuthorizer, error) {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrPolicyDenied
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrPolicyDenied
	}
	return emulatorAuthorization{}, nil
}

// CreateWire exposes the validated canonical OneTime body for exact capability
// matching; no caller can authorize Withdrawal by labelling it a small payout.
func (t TaskTerms) CreateWire() (json.RawMessage, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(t.wire())
}
