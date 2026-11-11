package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// The manifest is private, reviewed input, never generated implicitly while
// executing. It contains no credentials. A run owns exactly one monetary task.
type rwCheckManifest struct {
	Version       int                         `json:"version"`
	RunID         string                      `json:"run_id"`
	Target        string                      `json:"target"`
	Origin        string                      `json:"origin"`
	AccountKey    string                      `json:"account_key"`
	CompanyINN    string                      `json:"company_inn"`
	Executor      rocketwork.ExecutorIdentity `json:"executor"`
	TaskID        int64                       `json:"task_id"`
	Terms         rocketwork.TaskTerms        `json:"terms"`
	TermsHash     string                      `json:"terms_sha256"`
	MaxCommission int64                       `json:"max_commission_kopecks"`
	Actions       []string                    `json:"actions"`
}
type rwCheckObservation struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
}
type rwCheckState struct {
	ManifestHash string               `json:"manifest_sha256"`
	TaskID       int64                `json:"task_id"`
	Operations   map[string]string    `json:"operations"`
	Observations []rwCheckObservation `json:"observations"`
}

func rwDigest(v any) string {
	b, _ := json.Marshal(v)
	var canonical any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	_ = d.Decode(&canonical)
	b, _ = json.Marshal(canonical)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func rwOrigin(target, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch target {
	case "production":
		return checkEndpoint(raw, rocketwork.DefaultBaseURL)
	case "sandbox":
		return false // No provider-approved RocketWork sandbox exists for this workflow.
	case "local-emulator":
		ip := net.ParseIP(u.Hostname())
		return (u.Scheme == "http" || u.Scheme == "https") && ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
func readRWJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid RocketWork run file")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid RocketWork run file")
	}
	return nil
}
func loadRWCheckManifest(c Config, o integrationCheckOptions) (rwCheckManifest, string, error) {
	var m rwCheckManifest
	invalid := errors.New("RocketWork manifest, target or approval mismatch")
	if err := readRWJSON(o.manifest, &m); err != nil {
		return m, "", err
	}
	if _, err := uuid.Parse(m.RunID); err != nil {
		return m, "", invalid
	}
	origin := c.RocketWork.BaseURL
	if origin == "" {
		origin = rocketwork.DefaultBaseURL
	}
	if m.Version != 1 || m.Target != o.target || m.Target != c.RocketWork.Target || !rwOrigin(m.Target, m.Origin) || !checkEndpoint(origin, strings.TrimSuffix(m.Origin, "/")) || m.AccountKey == "" || m.AccountKey != c.RocketWork.AccountKey || m.CompanyINN == "" || c.RocketWorkMode() != RocketWorkLive || strings.TrimSpace(c.RocketWork.Token) == "" || math.IsNaN(c.RocketWork.Rate) || math.IsInf(c.RocketWork.Rate, 0) || c.RocketWork.Rate < 0 {
		return m, "", invalid
	}
	if m.Executor.Validate() != nil || m.Executor.ExpectedID <= 0 || m.Executor.Kind != "inn" || m.Executor.Value != m.Terms.ExecutorINN || m.Executor.EmploymentKind != m.Terms.EmploymentKind || m.Terms.Validate() != nil || m.Terms.ExternalID != "integration-check:"+m.RunID || m.TermsHash != rwDigest(m.Terms) || m.MaxCommission < 0 || m.Terms.AmountKopecks > math.MaxInt64-m.MaxCommission || m.TaskID < 0 || m.Terms.Autopayable || m.Terms.Autocomplete || m.Terms.Autosignable {
		return m, "", invalid
	}
	seen := map[string]bool{}
	for _, a := range m.Actions {
		if seen[a] || !slices.Contains([]string{"create", "complete", "sign_act", "pay", "cancel"}, a) {
			return m, "", invalid
		}
		seen[a] = true
	}
	if len(m.Actions) == 0 || (seen["cancel"] && (seen["pay"] || seen["complete"] || seen["sign_act"])) || (m.TaskID == 0 && !seen["create"]) || (m.TaskID > 0 && seen["create"]) {
		return m, "", invalid
	}
	hash := rwDigest(m)
	if (o.scenario == "execute" || o.scenario == "resume") && (!o.execute || o.approvedHash != hash) {
		return m, "", errors.New("explicit execute and reviewed manifest SHA256 required")
	}
	return m, hash, nil
}

func saveRWCheckState(dir string, s *rwCheckState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".rw-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
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
func openRWCheckState(dir, hash string, m rwCheckManifest) (*rwCheckState, func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	lock := filepath.Join(dir, "run.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	_ = f.Close()
	unlock := func() { _ = os.Remove(lock) }
	s := &rwCheckState{ManifestHash: hash, TaskID: m.TaskID, Operations: map[string]string{}}
	err = readRWJSON(filepath.Join(dir, "state.json"), s)
	if errors.Is(err, os.ErrNotExist) {
		err = saveRWCheckState(dir, s)
	}
	if err == nil && (s.ManifestHash != hash || s.Operations == nil || (m.TaskID > 0 && s.TaskID != m.TaskID)) {
		err = errors.New("run state does not match reviewed manifest")
	}
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return s, unlock, nil
}

func (in *infra) checkRWClient(c Config) *rocketwork.Client {
	out := outbound.New(in.pool, in.clock, in.log, outbound.Options{System: outbound.SystemRocketWork, HTTP: rocketwork.NewHTTPClient(), NoRetry: true})
	lim := ratelimit.New(in.pool, in.clock, in.log, ratelimit.Options{})
	var authorization rocketwork.WriteAuthorizer
	if c.RocketWork.Target == "local-emulator" {
		authorization, _ = rocketwork.LocalEmulatorAuthorization(c.RocketWork.BaseURL)
	}
	return rocketwork.New(out, lim, rocketwork.Options{BaseURL: c.RocketWork.BaseURL, Token: c.RocketWork.Token, WebhookSecret: c.RocketWorkWebhookSecret(), Rate: c.RocketWork.Rate, Authorizer: authorization}, in.log)
}
func (in *infra) executeRWCheck(ctx context.Context, c Config, o integrationCheckOptions) error {
	m, hash, err := loadRWCheckManifest(c, o)
	if err != nil {
		return err
	}
	s, unlock, err := openRWCheckState(o.runDir, hash, m)
	if err != nil {
		return err
	}
	defer unlock()
	before := len(s.Operations)
	err = advanceRWCheck(ctx, in.checkRWClient(c), o, m, s, in.clock.Now)
	in.log.InfoContext(ctx, "rocketwork diagnostic finished", "run_id", m.RunID, "target", m.Target, "live_verified", false, "write_attempted", len(s.Operations) > before, "observations", len(s.Observations), "succeeded", err == nil)
	return err
}

func rwTaskMatches(t rocketwork.TaskSnapshot, m rwCheckManifest) bool {
	return t.ID > 0 && t.Type == "OneTime" && t.ExternalID == m.Terms.ExternalID && t.ExecutorID == m.Executor.ExpectedID && t.ExecutorINN == m.Terms.ExecutorINN && t.EmploymentKind == m.Terms.EmploymentKind && t.AmountKopecks == m.Terms.AmountKopecks && t.ActRequired != nil && *t.ActRequired == m.Terms.ActRequired && t.TaxRetention != nil && *t.TaxRetention == m.Terms.TaxRetention && t.SignatureForm == m.Terms.SignatureForm
}
func rwActionObserved(action, status string) bool {
	switch action {
	case "create":
		return true
	case "complete":
		return slices.Contains([]string{"accepting", "completed", "paying", "paid"}, status)
	case "sign_act":
		return slices.Contains([]string{"completed", "paying", "paid"}, status)
	case "pay":
		return status == "paying" || status == "paid"
	case "cancel":
		return status == "canceled"
	}
	return false
}

// Each invocation performs at most one side effect. An uncertain action is
// never repeated, even when a lookup is empty. Reconcile and inspect only read.
func advanceRWCheck(ctx context.Context, c *rocketwork.Client, o integrationCheckOptions, m rwCheckManifest, s *rwCheckState, now func() time.Time) error {
	a, err := c.Account(ctx)
	if err != nil {
		return err
	}
	if a.CompanyINN != m.CompanyINN {
		return errors.New("provider account differs from manifest")
	}
	if err := c.Ping(ctx); err != nil {
		return err
	}
	e, err := c.LookupExecutor(ctx, m.Executor)
	if err != nil {
		return err
	}
	var task rocketwork.TaskSnapshot
	found := false
	if s.TaskID > 0 {
		task, err = c.GetTask(ctx, s.TaskID)
		found = err == nil
	} else {
		task, found, err = c.FindTask(ctx, m.Terms.ExternalID)
	}
	if err != nil {
		return err
	}
	if found {
		if !rwTaskMatches(task, m) {
			return errors.New("provider task differs from manifest")
		}
		if task.CommissionKopecks != nil && *task.CommissionKopecks > m.MaxCommission {
			return errors.New("commission exceeds approved limit")
		}
		if task.Status == "paid" && (task.CommissionKopecks == nil || task.ExecutorAmountKopecks == nil || *task.ExecutorAmountKopecks != m.Terms.AmountKopecks || (m.Terms.TaxRetention && task.RetainedTaxKopecks == nil)) {
			return errors.New("paid task financial evidence incomplete")
		}
		s.TaskID = task.ID
		status := task.Status
		if !slices.Contains([]string{"draft", "inwork", "accepting", "completed", "paying", "paid", "canceled", "error"}, status) {
			status = "unknown"
		}
		s.Observations = append(s.Observations, rwCheckObservation{At: now(), Status: status})
		if len(s.Observations) > 256 {
			s.Observations = s.Observations[len(s.Observations)-256:]
		}
		for action, state := range s.Operations {
			if state == "uncertain" && rwActionObserved(action, task.Status) {
				s.Operations[action] = "confirmed"
			}
		}
		if err := saveRWCheckState(o.runDir, s); err != nil {
			return err
		}
	}
	if o.scenario == "inspect" || o.scenario == "reconcile" {
		return nil
	}
	for _, action := range m.Actions {
		if s.Operations[action] == "confirmed" {
			continue
		}
		if s.Operations[action] != "" {
			return errors.New("operation outcome uncertain; reconcile without repeating write")
		}
		if found && rwActionObserved(action, task.Status) {
			s.Operations[action] = "confirmed"
			if err := saveRWCheckState(o.runDir, s); err != nil {
				return err
			}
			continue
		}
		if action != "create" && !found {
			return errors.New("task not visible; no action permitted")
		}
		allowed := action == "create" && !found
		switch action {
		case "complete":
			allowed = task.Status == "inwork" && task.CustomerSigned != nil && *task.CustomerSigned && task.ExecutorSigned != nil && *task.ExecutorSigned
		case "sign_act":
			allowed = task.Status == "accepting" && m.Terms.ActRequired
		case "pay":
			allowed = task.Status == "completed"
		case "cancel":
			allowed = (task.Status == "draft" || task.Status == "inwork") && task.CustomerSigned != nil && !*task.CustomerSigned && task.ExecutorSigned != nil && !*task.ExecutorSigned
		}
		if !allowed {
			return errors.New("approved action precondition not satisfied")
		}
		if e.Status != rocketwork.ContractorActive || a.BalanceKopecks < m.Terms.AmountKopecks+m.MaxCommission {
			return errors.New("executor readiness or balance prevents write")
		}
		// Durable intent precedes every network side effect, including requests
		// whose response fails validation or cannot be saved locally.
		s.Operations[action] = "uncertain"
		if err := saveRWCheckState(o.runDir, s); err != nil {
			return err
		}
		switch action {
		case "create":
			task, err = c.CreateTask(ctx, m.Terms)
		case "complete":
			task, err = c.CompleteTask(ctx, s.TaskID)
		case "sign_act":
			task, err = c.SignTaskAct(ctx, s.TaskID)
		case "pay":
			task, err = c.PayTask(ctx, s.TaskID)
		case "cancel":
			task, err = c.CancelTask(ctx, s.TaskID)
		}
		if err != nil {
			return err
		}
		if !rwTaskMatches(task, m) {
			return errors.New("write response differs from manifest; reconcile")
		}
		s.TaskID = task.ID // response is a locator; the next GET confirms the effect
		return saveRWCheckState(o.runDir, s)
	}
	return nil
}
