package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
)

var _ domain.WorkflowStore = (*Store)(nil)

func (s *Store) workflowError(ctx context.Context, op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && (pgerr.Code == "23505" || pgerr.Code == "23514") {
		s.log.WarnContext(ctx, "rocketwork workflow constraint rejected", "op", op, "sqlstate", pgerr.Code)
		return domain.ErrConcurrentUpdate
	}
	// PostgreSQL details may contain lookup values or complete snapshots.
	s.log.ErrorContext(ctx, "rocketwork workflow storage failed", "op", op)
	return fmt.Errorf("rocketwork workflow storage: %s", op)
}

func bindingOf(r sqlc.RocketworkExecutorBinding) domain.ExecutorBinding {
	return domain.ExecutorBinding{Scope: domain.ProviderScope{Environment: r.Environment, Account: r.AccountKey}, OrganizationID: r.OrganizationID, ExecutorID: r.ExecutorID, LookupKind: r.LookupKind, LookupValue: r.LookupValue, EmploymentKind: r.EmploymentKind, VerifiedAt: r.VerifiedAt}
}

func (s *Store) BindExecutor(ctx context.Context, b domain.ExecutorBinding) (domain.ExecutorBinding, error) {
	if err := b.Validate(); err != nil {
		return domain.ExecutorBinding{}, err
	}
	r, err := s.q(ctx).BindRocketworkExecutor(ctx, sqlc.BindRocketworkExecutorParams{Environment: b.Scope.Environment, AccountKey: b.Scope.Account, OrganizationID: b.OrganizationID, ExecutorID: b.ExecutorID, LookupKind: b.LookupKind, LookupValue: b.LookupValue, EmploymentKind: b.EmploymentKind, VerifiedAt: b.VerifiedAt})
	if err != nil {
		return domain.ExecutorBinding{}, s.workflowError(ctx, "bind_executor", err)
	}
	s.log.InfoContext(ctx, "rocketwork executor binding verified", "organization_id", b.OrganizationID, "environment", b.Scope.Environment)
	return bindingOf(r), nil
}

func (s *Store) ExecutorBinding(ctx context.Context, scope domain.ProviderScope, org uuid.UUID) (domain.ExecutorBinding, error) {
	r, err := s.q(ctx).GetRocketworkBinding(ctx, sqlc.GetRocketworkBindingParams{Environment: scope.Environment, AccountKey: scope.Account, OrganizationID: org})
	if err != nil {
		return domain.ExecutorBinding{}, s.workflowError(ctx, "get_binding", err)
	}
	return bindingOf(r), nil
}

func workflowOf(r sqlc.RocketworkWorkflow) (domain.Workflow, error) {
	w := domain.Workflow{ObservedStatus: r.ObservedStatus, PayoutID: r.PayoutID, Scope: domain.ProviderScope{Environment: r.Environment, Account: r.AccountKey}, ExternalID: r.ExternalID, TaskID: r.TaskID, TermsHash: r.TermsHash, Phase: r.Phase, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if err := json.Unmarshal(r.Terms, &w.Terms); err != nil {
		return domain.Workflow{}, domain.ErrInvalidPayout
	}
	if err := json.Unmarshal(r.Documents, &w.Documents); err != nil {
		return domain.Workflow{}, domain.ErrInvalidPayout
	}
	if w.Phase != "legacy" {
		_, hash, err := w.Terms.Snapshot()
		if err != nil || hash != w.TermsHash {
			return domain.Workflow{}, fmt.Errorf("%w: workflow snapshot integrity", domain.ErrInvalidPayout)
		}
	}
	return w, nil
}

func (s *Store) PrepareWorkflow(ctx context.Context, w domain.Workflow) (domain.Workflow, error) {
	if err := w.Scope.Validate(); err != nil {
		return domain.Workflow{}, err
	}
	body, hash, err := w.Terms.Snapshot()
	if err != nil {
		return domain.Workflow{}, err
	}
	r, err := s.q(ctx).PrepareRocketworkWorkflow(ctx, sqlc.PrepareRocketworkWorkflowParams{ID: w.PayoutID, Environment: w.Scope.Environment, AccountKey: w.Scope.Account, Terms: body, TermsHash: hash, CreatedAt: w.CreatedAt, ExecutorID: w.Terms.ExecutorID, EmploymentKind: w.Terms.EmploymentKind, Amount: int64(w.Terms.Amount)})
	if err != nil {
		return domain.Workflow{}, s.workflowError(ctx, "prepare_workflow", err)
	}
	s.log.InfoContext(ctx, "rocketwork workflow prepared", "payout_id", w.PayoutID, "terms_hash", hash)
	return workflowOf(r)
}

func (s *Store) Workflow(ctx context.Context, id uuid.UUID, lock bool) (domain.Workflow, error) {
	var r sqlc.RocketworkWorkflow
	var err error
	if lock {
		r, err = s.q(ctx).LockRocketworkWorkflow(ctx, id)
	} else {
		r, err = s.q(ctx).GetRocketworkWorkflow(ctx, id)
	}
	if err != nil {
		return domain.Workflow{}, s.workflowError(ctx, "get_workflow", err)
	}
	return workflowOf(r)
}

func (s *Store) UpdateWorkflow(ctx context.Context, w domain.Workflow) error {
	documents, err := json.Marshal(w.Documents)
	if err != nil {
		return domain.ErrInvalidPayout
	}
	n, err := s.q(ctx).UpdateRocketworkWorkflow(ctx, sqlc.UpdateRocketworkWorkflowParams{ObservedStatus: w.ObservedStatus, PayoutID: w.PayoutID, TaskID: w.TaskID, Phase: w.Phase, UpdatedAt: w.UpdatedAt, Documents: documents})
	if err != nil {
		return s.workflowError(ctx, "update_workflow", err)
	}
	if n != 1 {
		return domain.ErrConcurrentUpdate
	}
	s.log.InfoContext(ctx, "rocketwork workflow phase changed", "payout_id", w.PayoutID, "phase", w.Phase)
	return nil
}

func operationOf(r sqlc.RocketworkOperation) domain.WorkflowOperation {
	return domain.WorkflowOperation{PayoutID: r.PayoutID, Action: r.Action, State: domain.OperationState(r.State), PayloadHash: r.PayloadHash, CorrelationID: r.CorrelationID, Evidence: r.Evidence, UpdatedAt: r.UpdatedAt}
}

func (s *Store) PrepareOperation(ctx context.Context, o domain.WorkflowOperation) (domain.WorkflowOperation, error) {
	r, err := s.q(ctx).PrepareRocketworkOperation(ctx, sqlc.PrepareRocketworkOperationParams{PayoutID: o.PayoutID, Action: o.Action, PayloadHash: o.PayloadHash, CorrelationID: o.CorrelationID, UpdatedAt: o.UpdatedAt})
	if err != nil {
		return domain.WorkflowOperation{}, s.workflowError(ctx, "prepare_operation", err)
	}
	s.log.DebugContext(ctx, "rocketwork operation checkpoint prepared", "payout_id", o.PayoutID, "action", o.Action)
	return operationOf(r), nil
}

func (s *Store) Operation(ctx context.Context, id uuid.UUID, action string) (domain.WorkflowOperation, error) {
	r, err := s.q(ctx).GetRocketworkOperation(ctx, sqlc.GetRocketworkOperationParams{PayoutID: id, Action: action})
	if err != nil {
		return domain.WorkflowOperation{}, s.workflowError(ctx, "get_operation", err)
	}
	return operationOf(r), nil
}

func (s *Store) TransitionOperation(ctx context.Context, o domain.WorkflowOperation, next domain.OperationState) (bool, error) {
	if !domain.CanTransitionOperation(o.State, next) {
		return false, domain.ErrInvalidPayoutState
	}
	n, err := s.q(ctx).TransitionRocketworkOperation(ctx, sqlc.TransitionRocketworkOperationParams{PayoutID: o.PayoutID, Action: o.Action, CorrelationID: o.CorrelationID, PreviousState: string(o.State), NextState: string(next), Evidence: o.Evidence, UpdatedAt: o.UpdatedAt})
	if err != nil {
		return false, s.workflowError(ctx, "transition_operation", err)
	}
	if n == 1 {
		s.log.InfoContext(ctx, "rocketwork operation transitioned", "payout_id", o.PayoutID, "action", o.Action, "from", o.State, "to", next)
	}
	return n == 1, nil
}
