package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type ordSandboxRun struct {
	Version   int              `json:"version"`
	RunID     string           `json:"run_id"`
	Endpoint  string           `json:"endpoint"`
	CreatedAt time.Time        `json:"created_at"`
	Steps     []ordSandboxStep `json:"steps"`
}

type ordSandboxStep struct {
	Name            string          `json:"name"`
	Operation       string          `json:"operation"`
	ObjectID        string          `json:"object_id"`
	Payload         json.RawMessage `json:"payload"`
	State           string          `json:"state"`
	RequestID       string          `json:"request_id,omitempty"`
	Status          string          `json:"status,omitempty"`
	DependencyReady bool            `json:"dependency_ready,omitempty"`
	Erid            string          `json:"erid,omitempty"`
	ErrorCodes      []string        `json:"error_codes,omitempty"`
	Validation      []string        `json:"validation,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at,omitempty"`
}

type ordSandboxAPI interface {
	SubmitSandbox(context.Context, string, string, json.RawMessage) (ord.Receipt, error)
	GetStatus(context.Context, string) (ord.Receipt, error)
}

var ordErrorCode = regexp.MustCompile(`\b[A-Z]{1,4}_[0-9]+\b`)

func openORDSandboxRun(dir string, now time.Time) (*ordSandboxRun, func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	lock := filepath.Join(dir, "run.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(lock)
		return nil, nil, err
	}
	unlock := func() { _ = os.Remove(lock) }
	fail := func(err error) (*ordSandboxRun, func(), error) { unlock(); return nil, nil, err }
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		r := newORDSandboxRun(newCheckRunID(), now)
		if err := saveORDSandboxRun(dir, r); err != nil {
			return fail(err)
		}
		return r, unlock, nil
	}
	if err != nil {
		return fail(err)
	}
	var r ordSandboxRun
	if err = json.Unmarshal(data, &r); err != nil {
		return fail(err)
	}
	if _, err = uuid.Parse(r.RunID); err != nil || r.Version != 1 || r.Endpoint != ord.TestBaseURL || r.CreatedAt.IsZero() || r.CreatedAt.After(now) || len(r.Steps) == 0 {
		return fail(errors.New("invalid ORD sandbox state"))
	}
	// Reject missing/reordered steps and foreign IDs. Payloads remain reviewable
	// for correcting a definite provider rejection; uncertain writes stay blocked.
	shape := newORDSandboxRun(r.RunID, r.CreatedAt)
	if len(r.Steps) != len(shape.Steps) {
		return fail(errors.New("incomplete ORD sandbox state"))
	}
	for i, s := range r.Steps {
		want := shape.Steps[i]
		if s.Name != want.Name || s.Operation != want.Operation || s.ObjectID != want.ObjectID || !sandboxPayloadIDsMatch(s.Payload, want.Payload) {
			return fail(errors.New("ORD sandbox state identity mismatch"))
		}
		switch s.State {
		case "prepared", "sending", "sent", "done", "failed":
		default:
			return fail(errors.New("invalid ORD step state"))
		}
		if (s.State == "sent" || s.State == "done") && s.RequestID == "" {
			return fail(errors.New("missing ORD receipt"))
		}
	}
	return &r, unlock, nil
}

// Compare every identifier path, including references in arrays, with the
// generated manifest. Editable fixtures cannot target objects from another run.
func sandboxPayloadIDsMatch(payload, expected json.RawMessage) bool {
	identifiers := func(raw json.RawMessage) (map[string]string, error) {
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		out := map[string]string{}
		var walk func(any, string)
		walk = func(value any, path string) {
			switch v := value.(type) {
			case map[string]any:
				for key, child := range v {
					childPath := path + "/" + key
					if key == "id" || strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "Ids") {
						encoded, _ := json.Marshal(child)
						out[childPath] = string(encoded)
					} else {
						walk(child, childPath)
					}
				}
			case []any:
				for i, child := range v {
					walk(child, fmt.Sprintf("%s/%d", path, i))
				}
			}
		}
		walk(body, "")
		return out, nil
	}
	got, err := identifiers(payload)
	if err != nil {
		return false
	}
	want, err := identifiers(expected)
	if err != nil || len(got) != len(want) {
		return false
	}
	for path, value := range want {
		if got[path] != value {
			return false
		}
	}
	return true
}

func saveORDSandboxRun(dir string, r *ordSandboxRun) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ord-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, "state.json"))
}

// One command advances all ready steps and polls each outstanding receipt once. Every POST is
// preceded by durable 'sending' state. Any uncertain outcome blocks resubmission.
func executeORDSandbox(ctx context.Context, api ordSandboxAPI, clk clock.Clock, log *slog.Logger, dir string) error {
	r, unlock, err := openORDSandboxRun(dir, clk.Now())
	if err != nil {
		return err
	}
	defer unlock()
	ctx = logx.With(ctx, "run_id", r.RunID)
	complete := true
	for i := range r.Steps {
		s := &r.Steps[i]
		if s.State == "done" {
			continue
		}
		if s.State == "sending" || s.State == "failed" {
			log.WarnContext(ctx, "ord sandbox step requires reconciliation", "step", s.Name, "state", s.State, "error_codes", s.ErrorCodes)
			return errors.New("ORD sandbox step blocked; no automatic resubmission")
		}
		if s.State == "prepared" && !ordSandboxDependenciesDone(r, s.Name) {
			complete = false
			continue
		}
		log.DebugContext(ctx, "ord sandbox step started", "step", s.Name, "state", s.State)
		var rec ord.Receipt
		if s.State == "prepared" {
			s.State = "sending"
			s.UpdatedAt = clk.Now()
			if err = saveORDSandboxRun(dir, r); err != nil {
				return err
			}
			rec, err = api.SubmitSandbox(ctx, s.Operation, s.ObjectID, s.Payload)
			if err != nil {
				var validation *ord.ValidationError
				if errors.As(err, &validation) {
					s.State = "failed"
					s.ErrorCodes = ordErrorCode.FindAllString(validation.Error(), -1)
					s.Validation = safeORDValidation(validation.Details, s.Payload)
				}
				if saveErr := saveORDSandboxRun(dir, r); saveErr != nil {
					return saveErr
				}
				return err
			}
			if rec.RequestID == "" {
				return errors.New("ORD accepted without receipt; outcome uncertain")
			}
			s.RequestID = rec.RequestID
			s.State = "sent"
		} else {
			rec, err = api.GetStatus(ctx, s.RequestID)
			if err != nil {
				return err
			}
			if rec.RequestID != s.RequestID {
				return errors.New("ORD poll receipt mismatch")
			}
		}
		applyORDSandboxReceipt(s, rec)
		s.UpdatedAt = clk.Now()
		if err = saveORDSandboxRun(dir, r); err != nil {
			return err
		}
		log.InfoContext(ctx, "ord sandbox step updated", "step", s.Name, "state", s.State, "ord_status", s.Status, "ord_request_id", s.RequestID, "dependency_ready", s.DependencyReady, "error_codes", s.ErrorCodes)
		if s.State == "failed" {
			return fmt.Errorf("ORD rejected sandbox step %s", s.Name)
		}
		if s.State != "done" {
			complete = false
			log.InfoContext(ctx, "ord sandbox waiting; resume with same run directory", "step", s.Name, "complete", false)
		}
	}
	if !complete {
		return nil
	}
	log.InfoContext(ctx, "ord sandbox fixture completed", "complete", true, "steps", len(r.Steps), "production_contract_verified", false)
	return nil
}

func ordSandboxDependenciesDone(r *ordSandboxRun, name string) bool {
	deps := map[string][]string{
		"advertiser": {}, "agent": {}, "publisher": {},
		"initial-contract":            {"advertiser", "agent"},
		"publisher-contract":          {"agent", "publisher"},
		"platform":                    {"publisher"},
		"creative":                    {"initial-contract"},
		"statistics":                  {"creative", "platform"},
		"publisher-invoice":           {"publisher-contract", "initial-contract", "statistics"},
		"publisher-invoice-creatives": {"publisher-invoice", "creative"},
		"agent-report":                {"initial-contract", "publisher-invoice-creatives"},
		"agent-report-creatives":      {"agent-report", "creative"},
	}
	required, ok := deps[name]
	if !ok {
		return false
	}
	for _, dependency := range required {
		found := false
		for _, step := range r.Steps {
			if step.Name == dependency && (step.State == "done" ||
				(sandboxReferenceOperation(step.Operation) && step.State == "sent" && step.DependencyReady && step.Status == "ERIR sync success")) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Validation diagnostics omit values from the synthetic fixture as well as
// long digit sequences; no request body or credentials are stored in errors.
func safeORDValidation(details []string, payload json.RawMessage) []string {
	var body any
	_ = json.Unmarshal(payload, &body)
	var values []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for _, value := range v {
				collect(value)
			}
		case []any:
			for _, value := range v {
				collect(value)
			}
		case string:
			if len(v) > 4 {
				values = append(values, v)
			}
		}
	}
	collect(body)
	digits := regexp.MustCompile(`[0-9]{10,}`)
	out := make([]string, 0, len(details))
	for _, detail := range details {
		for _, value := range values {
			detail = strings.ReplaceAll(detail, value, "[REDACTED]")
		}
		out = append(out, digits.ReplaceAllString(detail, "[REDACTED]"))
	}
	return out
}

func applyORDSandboxReceipt(s *ordSandboxStep, rec ord.Receipt) {
	s.Status = rec.Status
	s.DependencyReady = false
	s.Validation = nil
	if rec.Token != "" {
		s.Erid = rec.Token
	}
	statuses := []string{rec.Status}
	errorsRaw := rec.Errors()
	for _, object := range rec.ObjectStatuses {
		statuses = append(statuses, object.Status)
		errorsRaw = append(errorsRaw, object.Errors()...)
		if object.Token != "" {
			s.Erid = object.Token
		}
	}
	s.ErrorCodes = nil
	s.Validation = safeORDValidation(errorsRaw, s.Payload)
	for _, e := range errorsRaw {
		s.ErrorCodes = append(s.ErrorCodes, ordErrorCode.FindAllString(e, -1)...)
	}
	allDone := true
	// Referenced organizations/contracts/platforms can be submitted to ORD
	// before final ERIR confirmation. This readiness is NOT final success:
	// continue polling them and keep creatives/reports behind final-success gates.
	referenceReady := sandboxReferenceOperation(s.Operation)
	for _, status := range statuses {
		if status != "ERIR sync success" && status != "ERIR async success" {
			referenceReady = false
		}
		switch status {
		case "ORD error", "ORD rejected", "ERIR sync error", "ERIR async error":
			s.State = "failed"
			return
		case "ERIR async success":
		case "ORD success":
			// Platforms have no ERIR identity. Other objects wait for ERIR rather
			// than assuming that ORD acceptance means reporting is complete.
			if s.Operation != ord.OpPlatforms {
				allDone = false
			}
		default:
			allDone = false
		}
	}
	s.DependencyReady = referenceReady
	if s.Operation == ord.OpCreative && s.Erid == "" {
		allDone = false
	}
	if allDone {
		s.State = "done"
	} else {
		s.State = "sent"
	}
}

func sandboxReferenceOperation(operation string) bool {
	return operation == ord.OpOrganization || operation == ord.OpContract || operation == ord.OpPlatforms
}
