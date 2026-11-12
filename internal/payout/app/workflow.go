package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func (s *Service) workflowWait(reason string) error {
	return job.Postpone(fmt.Errorf("rocketwork workflow: %s", reason), s.Config.PollFast)
}

// DrivePayout advances at most one external write. Every write has an immutable
// checkpoint committed first. All recovery paths read the same external ID.
func (s *Service) DrivePayout(ctx context.Context, id uuid.UUID) error {
	store, ok := s.Store.(domain.WorkflowStore)
	if !ok || !s.Config.ProviderEnabled || s.Config.ProviderScope == nil {
		return domain.ErrProviderDisabled
	}
	provider, ok := s.Provider.(domain.TaskProvider)
	if !ok {
		return domain.ErrProviderDisabled
	}
	p, err := s.Store.PayoutByID(ctx, id, false)
	if err != nil {
		return err
	}
	if p.Method != domain.MethodRocketwork {
		return nil
	}
	w, err := store.Workflow(ctx, id, false)
	if errors.Is(err, domain.ErrNotFound) {
		if err = s.beforeWorkflowWrite(ctx, p, s.Config.AgreementTemplate); err != nil {
			return err
		}
		err = s.Tx.InTx(ctx, func(ctx context.Context) error {
			locked, err := s.Store.PayoutByID(ctx, id, true)
			if err != nil {
				return err
			}
			if existing, e := store.Workflow(ctx, id, false); e == nil {
				w = existing
				return nil
			} else if !errors.Is(e, domain.ErrNotFound) {
				return e
			}
			if locked.Status != domain.StatusCreated {
				return domain.ErrInvalidPayoutState
			}
			w, err = s.preparePayoutWorkflow(ctx, locked)
			return err
		})
	}
	if err != nil {
		return err
	}
	if w.Phase == "legacy" {
		return s.workflowWait("legacy identity requires manual reconciliation")
	}
	if w.Scope != *s.Config.ProviderScope {
		return domain.ErrInvalidPayout
	}
	b, err := store.ExecutorBinding(ctx, w.Scope, p.BloggerOrganizationID)
	if err != nil {
		return err
	}
	if w.TaskID == nil {
		return s.createWorkflowTask(ctx, store, provider, p, w, b)
	}
	t, err := provider.ReadTask(ctx, *w.TaskID)
	if err != nil {
		return err
	}
	if err = w.ValidateTask(t, b); err != nil {
		s.log.WarnContext(ctx, "rocketwork task reconciliation mismatch", "payout_id", id)
		return err
	}
	if err = s.reconcileOperations(ctx, store, w, t); err != nil {
		return err
	}
	{
		if err := s.saveWorkflowDocuments(ctx, store, p.ID, t.Documents, t.Status); err != nil {
			return err
		}
	}
	switch t.Status {
	case "paid":
		return s.finishWorkflowPayment(ctx, p, w, t)
	case "canceled":
		if p.Status == domain.StatusSucceeded {
			return nil
		}
		return s.Tx.InTx(ctx, func(ctx context.Context) error {
			current, err := s.Store.PayoutByID(ctx, p.ID, true)
			if err != nil {
				return err
			}
			if current.Status == domain.StatusSucceeded {
				return nil
			}
			latest, err := store.Workflow(ctx, p.ID, true)
			if err != nil {
				return err
			}
			latest.Phase, latest.UpdatedAt = "canceled", s.Clock.Now()
			if err := store.UpdateWorkflow(ctx, latest); err != nil {
				return err
			}
			return s.fail(ctx, p.ID, domain.FailureProviderError, "provider task canceled")
		})
	}
	if p.Status.Final() {
		return s.workflowWait("final payout requires reconciliation")
	}
	action := domain.NextTaskAction(t)
	if w.Phase == "cancel_requested" {
		if (t.Status != "draft" && t.Status != "inwork") || t.CustomerSigned == nil || *t.CustomerSigned || t.ExecutorSigned == nil || *t.ExecutorSigned {
			return s.workflowWait("cancellation requires unsigned task")
		}
		return s.performTaskAction(ctx, store, p, w, "cancel")
	}
	if action == "" {
		return s.workflowWait("waiting for provider or executor")
	}
	if err := s.beforeWorkflowWrite(ctx, p, &w.Terms); err != nil {
		return err
	}
	return s.performTaskAction(ctx, store, p, w, action)
}

func (s *Service) saveWorkflowDocuments(ctx context.Context, store domain.WorkflowStore, id uuid.UUID, documents domain.PayoutDocuments, status string) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Store.PayoutByID(ctx, id, true); err != nil {
			return err
		}
		w, err := store.Workflow(ctx, id, true)
		if err != nil {
			return err
		}
		merged, err := w.Documents.Merge(documents)
		if err != nil {
			s.log.WarnContext(ctx, "rocketwork document response rejected", "payout_id", id)
			return nil
		}
		w.Documents, w.UpdatedAt = merged, s.Clock.Now()
		w.ObservedStatus = knownTaskStatus(status)
		return store.UpdateWorkflow(ctx, w)
	})
}

func (s *Service) beforeWorkflowWrite(ctx context.Context, p domain.Payout, terms *domain.AgreementTerms) error {
	info, err := s.Placements.Info(ctx, p.PlacementID)
	if err != nil {
		return err
	}
	if info.Status != domain.PlacementStatusAccepted || info.BloggerOrgID != p.BloggerOrganizationID || info.PayoutAmount != p.Amount {
		return domain.ErrPlacementState
	}
	c, err := s.Store.ContractorByID(ctx, p.ContractorID, false)
	if err != nil {
		return err
	}
	pc, err := s.resolveContractor(ctx, c)
	if err != nil {
		return err
	}
	if _, err = s.applyContractor(ctx, c.ID, pc); err != nil {
		return err
	}
	if pc.Status != domain.ContractorActive {
		return s.workflowWait("executor not ready")
	}
	if terms == nil {
		return fmt.Errorf("%w: agreement policy missing", domain.ErrInvalidPayout)
	}
	fee := terms.MaxCommission
	if fee < 0 || int64(p.Amount) > math.MaxInt64-int64(fee) {
		return domain.ErrInvalidPayout
	}
	deposit, err := s.Provider.GetDepositBalance(ctx)
	if err != nil {
		return err
	}
	if deposit < p.Amount+fee {
		return s.depositShort(ctx, p, int64(deposit))
	}
	return nil
}

func (s *Service) createWorkflowTask(ctx context.Context, store domain.WorkflowStore, provider domain.TaskProvider, p domain.Payout, w domain.Workflow, b domain.ExecutorBinding) error {
	o, err := store.PrepareOperation(ctx, domain.WorkflowOperation{PayoutID: p.ID, Action: "create", PayloadHash: w.TermsHash, CorrelationID: ids.New(), UpdatedAt: s.Clock.Now()})
	if err != nil {
		return err
	}
	if o.State != domain.OperationPrepared {
		if o.State == domain.OperationRejected {
			return s.workflowWait("creation rejected; operator review required")
		}
		t, found, err := provider.FindTask(ctx, w.ExternalID)
		if err != nil {
			return err
		}
		if !found {
			return s.workflowWait("creation outcome unknown; absence is not proof")
		}
		if err := w.ValidateTask(t, b); err != nil {
			return err
		}
		return s.attachWorkflowTask(ctx, store, p, w, o, t)
	}
	if err := s.beforeWorkflowWrite(ctx, p, &w.Terms); err != nil {
		return err
	}
	claimed, err := s.claimWorkflowOperation(ctx, store, p, o)
	if err != nil {
		return err
	}
	if !claimed {
		return s.workflowWait("operation claimed elsewhere")
	}
	o.State = domain.OperationInFlight
	t, callErr := provider.CreateTask(ctx, domain.TaskOrder{ExternalID: w.ExternalID, ExecutorINN: b.LookupValue, Terms: w.Terms})
	if callErr == nil {
		callErr = w.ValidateTask(t, b)
	}
	if callErr != nil {
		return s.saveUnknownOperation(ctx, store, o, callErr)
	}
	return s.attachWorkflowTask(ctx, store, p, w, o, t)
}

func (s *Service) attachWorkflowTask(ctx context.Context, store domain.WorkflowStore, p domain.Payout, w domain.Workflow, o domain.WorkflowOperation, t domain.ProviderTask) error {
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		locked, err := s.Store.PayoutByID(ctx, p.ID, true)
		if err != nil {
			return err
		}
		current, err := store.Workflow(ctx, p.ID, true)
		if err != nil {
			return err
		}
		if current.TaskID != nil {
			if *current.TaskID != t.ID {
				return domain.ErrInvalidPayout
			}
			return nil
		}
		w.TaskID = &t.ID
		w.Phase = "agreement"
		w.UpdatedAt = s.Clock.Now()
		if err := store.UpdateWorkflow(ctx, w); err != nil {
			return err
		}
		o.UpdatedAt = s.Clock.Now()
		o.Evidence = "task_identity_verified"
		if o.State != domain.OperationConfirmed {
			if _, err := store.TransitionOperation(ctx, o, domain.OperationConfirmed); err != nil {
				return err
			}
		}
		if _, err := locked.MarkSent(strconv.FormatInt(t.ID, 10), s.Clock.Now()); err != nil {
			return err
		}
		if err := s.Store.UpdatePayout(ctx, &locked); err != nil {
			return err
		}
		return s.Jobs.Enqueue(ctx, job.Job{Kind: KindPollPayout, DedupeKey: PollPayoutKey(p.ID), Payload: PayoutPayload{p.ID}, RunAt: s.Clock.Now().Add(s.Config.PollFast), MaxAttempts: PollAttempts})
	})
	if err != nil {
		return err
	}
	return s.workflowWait("agreement created; awaiting authoritative state")
}

func (s *Service) saveUnknownOperation(ctx context.Context, store domain.WorkflowStore, o domain.WorkflowOperation, cause error) error {
	next := domain.OperationUncertain
	o.Evidence = "unconfirmed"
	if _, rejected := domain.AsRejected(cause); rejected {
		next = domain.OperationRejected
		o.Evidence = "provider_rejection"
	}
	o.UpdatedAt = s.Clock.Now()
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := store.TransitionOperation(saveCtx, o, next); err != nil {
		return err
	}
	s.log.WarnContext(ctx, "rocketwork action needs reconciliation", "payout_id", o.PayoutID, "action", o.Action, "state", next)
	return s.workflowWait("action not confirmed")
}

func (s *Service) reconcileOperations(ctx context.Context, store domain.WorkflowStore, w domain.Workflow, t domain.ProviderTask) error {
	for _, action := range []string{"create", "complete", "sign_act", "pay", "cancel"} {
		o, err := store.Operation(ctx, w.PayoutID, action)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if (o.State == domain.OperationInFlight || o.State == domain.OperationUncertain) && domain.OperationObserved(action, t.Status) {
			o.Evidence = "observed_by_get"
			o.UpdatedAt = s.Clock.Now()
			if _, err := store.TransitionOperation(ctx, o, domain.OperationConfirmed); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) performTaskAction(ctx context.Context, store domain.WorkflowStore, p domain.Payout, w domain.Workflow, action string) error {
	api, ok := s.Provider.(domain.TaskActions)
	if !ok {
		return domain.ErrProviderDisabled
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(w.TermsHash+":"+strconv.FormatInt(*w.TaskID, 10)+":"+action)))
	o, err := store.PrepareOperation(ctx, domain.WorkflowOperation{PayoutID: p.ID, Action: action, PayloadHash: hash, CorrelationID: ids.New(), UpdatedAt: s.Clock.Now()})
	if err != nil {
		return err
	}
	if o.State != domain.OperationPrepared {
		return s.workflowWait("action already attempted; read reconciliation required")
	}
	claimed, err := s.claimWorkflowOperation(ctx, store, p, o)
	if err != nil {
		return err
	}
	if !claimed {
		return s.workflowWait("action claimed elsewhere")
	}
	o.State = domain.OperationInFlight
	_, callErr := api.ActOnTask(ctx, *w.TaskID, action)
	// Even a 200 is reconciled by GET, because examples have inconsistent states.
	if callErr == nil {
		callErr = errors.New("awaiting authoritative read")
	}
	return s.saveUnknownOperation(ctx, store, o, callErr)
}

func (s *Service) finishWorkflowPayment(ctx context.Context, p domain.Payout, w domain.Workflow, t domain.ProviderTask) error {
	if t.Commission == nil || t.ExecutorAmount == nil {
		return s.workflowWait("paid; waiting for financial details")
	}
	if !w.Terms.AllowsCommission(*t.Commission) || *t.ExecutorAmount != w.Terms.Amount {
		s.log.WarnContext(ctx, "rocketwork financial reconciliation mismatch", "payout_id", p.ID)
		return domain.ErrInvalidPayout
	}
	if w.Terms.TaxRetention && t.RetainedTax == nil {
		return s.workflowWait("paid; waiting for retained tax")
	}
	if t.RetainedTax != nil && (*t.RetainedTax < 0 || *t.RetainedTax > *t.ExecutorAmount || (!w.Terms.TaxRetention && *t.RetainedTax != 0)) {
		return domain.ErrInvalidPayout
	}
	store := s.Store.(domain.WorkflowStore)
	ready := false
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.Store.PayoutByID(ctx, p.ID, true)
		if err != nil {
			return err
		}
		latest, err := store.Workflow(ctx, p.ID, true)
		if err != nil {
			return err
		}
		incoming := t.Documents
		incoming.ReceiptURL = t.ReceiptURL
		documents, docErr := latest.Documents.Merge(incoming)
		if docErr != nil {
			documents = latest.Documents
			s.log.WarnContext(ctx, "rocketwork document response rejected", "payout_id", p.ID)
		}
		receipt := documents.ReceiptURL
		if receipt == "" && len(documents.FNS) > 0 {
			receipt = documents.FNS[0].URL
		}
		if current.Status == domain.StatusSucceeded {
			if current.ProviderFee != *t.Commission {
				return domain.ErrInvalidPayoutState
			}
			changed, err := current.AttachReceipt(receipt, s.Clock.Now())
			if err != nil {
				return err
			}
			if changed {
				if err := s.Store.UpdatePayout(ctx, &current); err != nil {
					return err
				}
			}
		} else {
			if err := s.complete(ctx, p.ID, domain.ProviderPayout{ExternalID: strconv.FormatInt(t.ID, 10), Status: domain.StatusSucceeded, Fee: *t.Commission, ReceiptURL: receipt}); err != nil {
				return err
			}
		}
		latest.Documents = documents
		latest.ObservedStatus = "paid"
		latest.Phase = "documents"
		latest.UpdatedAt = s.Clock.Now()
		ready = docErr == nil && documents.Ready(w.Terms.EmploymentKind)
		if ready {
			latest.Phase = "complete"
		}
		return store.UpdateWorkflow(ctx, latest)
	})
	if err != nil {
		return err
	}
	if !ready {
		s.log.InfoContext(ctx, "rocketwork payment settled; documents pending", "payout_id", p.ID)
		return s.workflowWait("payment settled; documents pending")
	}
	s.log.InfoContext(ctx, "rocketwork payment reconciled", "payout_id", p.ID)
	return nil
}

func (s *Service) claimWorkflowOperation(ctx context.Context, store domain.WorkflowStore, p domain.Payout, o domain.WorkflowOperation) (bool, error) {
	claimed := false
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.Store.PayoutByID(ctx, p.ID, true)
		if err != nil {
			return err
		}
		if current.Status.Final() {
			return domain.ErrInvalidPayoutState
		}
		w, err := store.Workflow(ctx, p.ID, true)
		if err != nil {
			return err
		}
		if w.Phase == "legacy" || w.Scope != *s.Config.ProviderScope {
			return domain.ErrInvalidPayout
		}
		if (w.Phase == "cancel_requested") != (o.Action == "cancel") {
			return domain.ErrInvalidPayoutState
		}
		info, err := s.Placements.Info(ctx, p.PlacementID)
		if err != nil {
			return err
		}
		if o.Action != "cancel" && (info.Status != domain.PlacementStatusAccepted || info.BloggerOrgID != p.BloggerOrganizationID || info.PayoutAmount != p.Amount) {
			return domain.ErrPlacementState
		}
		claimed, err = store.TransitionOperation(ctx, o, domain.OperationInFlight)
		if err != nil || !claimed {
			return err
		}
		switch o.Action {
		case "complete", "sign_act":
			w.Phase = "acceptance"
		case "pay":
			w.Phase = "payment"
		}
		w.UpdatedAt = s.Clock.Now()
		return store.UpdateWorkflow(ctx, w)
	})
	return claimed, err
}
