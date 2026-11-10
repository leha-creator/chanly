package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworktest"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	payoutjobs "github.com/leha-creator/chanly/internal/payout/jobs"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func attachContractClient(t *testing.T, e *env, emulator *rocketworktest.Emulator, serverURL string) {
	t.Helper()
	log := logx.Discard()
	out := outbound.New(e.d.Pool, e.clk, log, outbound.Options{System: outbound.SystemRocketWork, HTTP: rocketwork.NewHTTPClient(), Sleep: func(context.Context, time.Duration) error { return nil }})
	lim := ratelimit.New(e.d.Pool, e.clk, log, ratelimit.Options{Sleep: func(_ context.Context, d time.Duration) error { e.clk.Advance(d); return nil }})
	c := rocketwork.New(out, lim, rocketwork.Options{BaseURL: serverURL, Token: emulator.Token, WebhookSecret: []byte("synthetic-postback-secret"), Rate: 10000}, log)
	e.svc.Provider = bridge.PayoutProvider{API: c, Log: log}
}

type failAfterCommittedClaim struct {
	tx.Runner
	store domain.WorkflowStore
	id    uuid.UUID
	fail  bool
}

func (r *failAfterCommittedClaim) InTx(ctx context.Context, fn func(context.Context) error) error {
	if err := r.Runner.InTx(ctx, fn); err != nil {
		return err
	}
	if r.fail {
		if op, err := r.store.Operation(ctx, r.id, "create"); err == nil && op.State == domain.OperationInFlight {
			r.fail = false
			return errors.New("synthetic process failure after committed claim")
		}
	}
	return nil
}
func TestContractCommittedCheckpointWithoutSendDoesNotPermitSecondAttempt(t *testing.T) {
	e, em, p, url := contractWorkflowEnv(t)
	ctx := context.Background()
	original := e.svc.Tx
	runner := &failAfterCommittedClaim{Runner: original, store: e.svc.Store.(domain.WorkflowStore), id: p.ID, fail: true}
	e.svc.Tx = runner
	if err := e.svc.DrivePayout(ctx, p.ID); err == nil || runner.fail {
		t.Fatal("committed checkpoint fault not injected")
	}
	e.svc.Tx = original
	attachContractClient(t, e, em, url)
	for i := 0; i < 3; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if em.WriteCount("POST", "/api/tasks") != 0 || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 0 {
		t.Fatal("unproven absence authorized a send after committed checkpoint")
	}
}

func TestContractOnboardingLostResponseBothLegalForms(t *testing.T) {
	for _, action := range []string{"invite", "register"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			legal, employment, path := accountdomain.SelfEmployed, "selfemployed", "/api/executors/invite"
			if action == "register" {
				legal, employment, path = accountdomain.SoleProprietor, "entrepreneur", "/api/executors"
			}
			org := ledgertest.Organization(t, e.d.Pool, legal, "synthetic")
			payee, err := e.svc.Organizations.ForPayout(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			em := rocketworktest.New(t)
			em.ConfigureExecutor(payee.INN, payee.Phone, employment, "all_is_well")
			em.ExecutorMissing(true)
			srv := httptest.NewServer(em)
			defer srv.Close()
			attachContractClient(t, e, em, srv.URL)
			e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
			if err := e.svc.RequestOnboarding(ctx, org, action, domain.RegistrationNames{FirstName: "Synthetic", LastName: "Fixture"}); err != nil {
				t.Fatal(err)
			}
			em.LoseNext("POST", path)
			if err := e.svc.RunOnboarding(ctx, org, action); !isPostponed(err) {
				t.Fatal(err)
			}
			if action == "invite" {
				if err := e.svc.RunOnboarding(ctx, org, action); !isPostponed(err) {
					t.Fatal(err)
				}
				em.ExecutorMissing(false) // executor accepts the SMS outside our system
			}
			attachContractClient(t, e, em, srv.URL)
			for i := 0; i < 2; i++ {
				if err := e.svc.RunOnboarding(ctx, org, action); err != nil {
					t.Fatal(err)
				}
			}
			if em.WriteCount("POST", path) != 1 || e.contractor(org).ExternalID != "7" {
				t.Fatal("onboarding repeated or identity lost")
			}
		})
	}
}

func TestContractWithdrawalLostResponseAndSettlement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	em := rocketworktest.New(t)
	dest := domain.WithdrawalDestination{CompanyINN: "000000000000", BIC: "000000000", AccountNumber: "00000000000000000000", ExecutorID: 7}
	em.ConfigureWithdrawal(dest.CompanyINN)
	srv := httptest.NewServer(em)
	defer srv.Close()
	attachContractClient(t, e, em, srv.URL)
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	e.svc.Config.WithdrawalDestination = &dest
	id := uuid.New()
	terms := domain.WithdrawalTerms{Destination: dest, Amount: 10000, MaxCommission: 100, AdminID: e.admin, ApproverID: ledgertest.User(t, e.d.Pool), Reason: "synthetic own funds"}
	if _, err := e.svc.PrepareWithdrawal(ctx, id, terms); err != nil {
		t.Fatal(err)
	}
	em.LoseNext("POST", "/api/tasks")
	if err := e.svc.ReconcileWithdrawal(ctx, id); !isPostponed(err) {
		t.Fatal(err)
	}
	em.HideLookups(1)
	attachContractClient(t, e, em, srv.URL)
	for i := 0; i < 2; i++ {
		if err := e.svc.ReconcileWithdrawal(ctx, id); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	w, err := e.svc.Store.(domain.WithdrawalStore).Withdrawal(ctx, id, false)
	if err != nil || w.TaskID == nil {
		t.Fatal("withdrawal not recovered", err)
	}
	em.SetTask(*w.TaskID, map[string]any{"status": "paid", "commission": "0.10"})
	for i := 0; i < 2; i++ {
		if err := e.svc.ReconcileWithdrawal(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if em.WriteCount("POST", "/api/tasks") != 1 || e.count("SELECT count(*) FROM ledger_transaction WHERE idempotency_key=$1", ledgerdomain.OperationKey(ledgerdomain.KindManual, "rocketwork-withdrawal:"+id.String())) != 1 {
		t.Fatal("duplicate withdrawal or ledger transaction")
	}
}

func contractWorkflowEnv(t *testing.T) (*env, *rocketworktest.Emulator, domain.Payout, string) {
	t.Helper()
	e, _, p := workflowEnv(t)
	payee, err := e.svc.Organizations.ForPayout(context.Background(), p.BloggerOrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	emulator := rocketworktest.New(t)
	emulator.ConfigureExecutor(payee.INN, payee.Phone, "selfemployed", "all_is_well")
	srv := httptest.NewServer(emulator)
	t.Cleanup(srv.Close)
	attachContractClient(t, e, emulator, srv.URL)
	return e, emulator, p, srv.URL
}

func TestContractWorkflowCrashRecoveryConcurrentPaymentAndLateReceipt(t *testing.T) {
	e, emulator, p, url := contractWorkflowEnv(t)
	ctx := context.Background()
	emulator.LoseNext("POST", "/api/tasks")
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	emulator.HideLookups(2)
	e.svc.SendExhausted(ctx, p.ID, errors.New("worker interrupted"))
	if retry, err := e.svc.Retry(ctx, p.ID, e.admin, "recover same task"); err != nil || retry.ID != p.ID {
		t.Fatal("retry changed monetary identity")
	}
	attachContractClient(t, e, emulator, url) // fresh transport/client, same durable checkpoints
	for i := 0; i < 3; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	w, err := e.svc.Store.(domain.WorkflowStore).Workflow(ctx, p.ID, false)
	if err != nil || w.TaskID == nil {
		t.Fatal("task not recovered")
	}
	id := *w.TaskID
	payPath := fmt.Sprintf("/api/tasks/%d/pay", id)
	emulator.LoseNext("PATCH", payPath)
	for tick := 0; tick < 4; tick++ {
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := e.svc.DrivePayout(ctx, p.ID)
				if err != nil && !isPostponed(err) && !errors.Is(err, domain.ErrConcurrentUpdate) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	if emulator.WriteCount("POST", "/api/tasks") != 1 || emulator.WriteCount("PATCH", payPath) != 1 {
		t.Fatal("recovery or concurrent workers repeated money write")
	}
	emulator.SetTask(id, map[string]any{"status": "paid"})
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 0 {
		t.Fatal("missing final fee posted")
	}
	emulator.SetTask(id, map[string]any{"commission": "0.10"})
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if e.payout(p.ID).Status != domain.StatusSucceeded || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 1 {
		t.Fatal("payment did not settle exactly once")
	}
	emulator.SetTask(id, map[string]any{"receipt_uri": "https://receipt.example.test/private-canary"})
	if err := e.svc.DrivePayout(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	emulator.SetTask(id, map[string]any{"status": "error"})
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if e.payout(p.ID).Status != domain.StatusSucceeded || emulator.WriteCount("PATCH", payPath) != 1 {
		t.Fatal("late event reverted or repaid payout")
	}
	var leaked int
	if err := e.d.Pool.QueryRow(ctx, "SELECT count(*) FROM outbound_request WHERE row_to_json(outbound_request)::text LIKE '%private-canary%' OR row_to_json(outbound_request)::text LIKE '%synthetic-emulator-token%'").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("private provider response persisted: %d %v", leaked, err)
	}
}

type crashAfterClaimStore struct {
	domain.Store
	domain.WorkflowStore
	crash bool
}

func (s *crashAfterClaimStore) TransitionOperation(ctx context.Context, o domain.WorkflowOperation, next domain.OperationState) (bool, error) {
	claimed, err := s.WorkflowStore.TransitionOperation(ctx, o, next)
	if err == nil && claimed && next == domain.OperationInFlight && s.crash {
		s.crash = false
		return false, errors.New("synthetic local failure after checkpoint")
	}
	return claimed, err
}

func TestContractWorkflowFailureBeforeSendDoesNotInventAbsenceProof(t *testing.T) {
	e, emulator, p, _ := contractWorkflowEnv(t)
	ctx := context.Background()
	store := &crashAfterClaimStore{Store: e.svc.Store, WorkflowStore: e.svc.Store.(domain.WorkflowStore), crash: true}
	// Claim lives inside a transaction. A failure returned here rolls it back,
	// therefore retry is safe and must perform one actual POST, not lose the task.
	e.svc.Store = store
	if err := e.svc.DrivePayout(ctx, p.ID); err == nil {
		t.Fatal("checkpoint failure hidden")
	}
	if emulator.WriteCount("POST", "/api/tasks") != 0 {
		t.Fatal("sent despite failed checkpoint")
	}
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if emulator.WriteCount("POST", "/api/tasks") != 1 {
		t.Fatal("rollback prevented safe retry")
	}
}

func TestContractCancellationLostResponseNeverPays(t *testing.T) {
	e, em, p, _ := contractWorkflowEnv(t)
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	w, err := e.svc.Store.(domain.WorkflowStore).Workflow(ctx, p.ID, false)
	if err != nil || w.TaskID == nil {
		t.Fatal(err)
	}
	id := *w.TaskID
	em.SetTask(id, map[string]any{"signed_at": nil, "executor_signed_at": nil})
	if err := e.svc.RequestCancellation(ctx, p.ID, e.admin, "synthetic cancellation"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d/cancel", id)
	em.LoseNext("PATCH", path)
	for i := 0; i < 3; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if em.WriteCount("PATCH", path) != 1 || em.WriteCount("PATCH", fmt.Sprintf("/api/tasks/%d/pay", id)) != 0 || e.payout(p.ID).Status != domain.StatusFailed || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 0 {
		t.Fatal("cancel repeated, paid or posted ledger")
	}
}

type failResponseStore struct {
	domain.Store
	domain.WorkflowStore
	fail bool
}

func (s *failResponseStore) UpdateWorkflow(ctx context.Context, w domain.Workflow) error {
	if s.fail && w.TaskID != nil {
		s.fail = false
		return errors.New("synthetic storage failure after provider effect")
	}
	return s.WorkflowStore.UpdateWorkflow(ctx, w)
}
func TestContractAcceptedTaskSurvivesLocalSaveFailure(t *testing.T) {
	e, em, p, url := contractWorkflowEnv(t)
	ctx := context.Background()
	original := e.svc.Store
	e.svc.Store = &failResponseStore{Store: original, WorkflowStore: original.(domain.WorkflowStore), fail: true}
	if err := e.svc.DrivePayout(ctx, p.ID); err == nil {
		t.Fatal("storage failure hidden")
	}
	e.svc.Store = original
	attachContractClient(t, e, em, url)
	em.HideLookups(1)
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	w, err := original.(domain.WorkflowStore).Workflow(ctx, p.ID, false)
	if err != nil || w.TaskID == nil || em.WriteCount("POST", "/api/tasks") != 1 {
		t.Fatal("lost local save caused second task", err)
	}
}

func TestContractHistoryAndReorderedCallbacksUseAuthoritativeState(t *testing.T) {
	e, em, p, _ := contractWorkflowEnv(t)
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	w, err := e.svc.Store.(domain.WorkflowStore).Workflow(ctx, p.ID, false)
	if err != nil || w.TaskID == nil {
		t.Fatal(err)
	}
	id := *w.TaskID
	em.SetTask(id, map[string]any{"status": "paid", "commission": "0.10", "receipt_uri": "https://receipt.example.test/synthetic"})
	paid := em.Event(id, "paid")
	late := em.Event(id, "error") // notification is stale; GET still reports paid
	through := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	e.clk.Advance(through.Sub(e.clk.Now()))
	log := logx.Discard()
	recorder := inbound.NewRecorder(e.d.Pool, e.tx, job.NewQueue(e.d.Pool, e.clk, log), e.clk, log)
	for _, body := range [][]byte{late, paid, late} {
		event, err := e.svc.ParseWebhook(map[string][]string{"Authorization": {"Bearer synthetic-postback-secret"}}, body)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(app.EventPayload{ExternalID: event.ExternalID, EventID: event.EventID, ScopeKey: event.ScopeKey})
		if _, err := recorder.Record(ctx, inbound.Event{Source: inbound.SourceRocketWork, ExternalKey: event.ScopeKey + ":" + event.EventID, EventType: string(event.Kind), Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := e.svc.RecoverPostbacks(ctx, recorder, through.Add(-2*time.Hour), through); err != nil {
			t.Fatal(err)
		}
	}
	if e.count("SELECT count(*) FROM inbound_event WHERE source='rocketwork'") != 2 {
		t.Fatal("callback/history replay not deduplicated")
	}
	dispatch := inbound.NewDispatcher(e.d.Pool, e.clk, log)
	payoutjobs.RegisterEvents(dispatch, e.svc)
	rows, err := e.d.Pool.Query(ctx, "SELECT id FROM inbound_event WHERE source='rocketwork' ORDER BY external_key DESC")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		raw, _ := json.Marshal(map[string]any{"inbound_event_id": id})
		if err := dispatch.Process(ctx, job.Running{Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if e.payout(p.ID).Status != domain.StatusSucceeded || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 1 || em.WriteCount("PATCH", fmt.Sprintf("/api/tasks/%d/pay", id)) != 0 {
		t.Fatal("stale callback changed money outcome")
	}
}
