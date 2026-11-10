package app

import (
	"context"
	"errors"

	"github.com/leha-creator/chanly/internal/payout/domain"
)

type WorkflowView struct {
	Stage, ProviderStatus, NextAction string
	Documents                         domain.PayoutDocuments
}

func knownTaskStatus(status string) string {
	switch status {
	case "draft", "inwork", "accepting", "completed", "paying", "paid", "error", "canceled":
		return status
	}
	return "unknown"
}

func (s *Service) workflowView(ctx context.Context, p domain.Payout) (*WorkflowView, error) {
	store, ok := s.Store.(domain.WorkflowStore)
	if !ok || p.Method != domain.MethodRocketwork {
		return nil, nil
	}
	w, err := store.Workflow(ctx, p.ID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &WorkflowView{Stage: w.Phase, ProviderStatus: w.ObservedStatus, Documents: w.Documents, NextAction: "wait"}
	if s.Config.ProviderScope == nil || w.Scope != *s.Config.ProviderScope {
		v.Stage, v.ProviderStatus, v.NextAction, v.Documents = "reconcile", "unknown", "contact_support", domain.PayoutDocuments{}
		return v, nil
	}
	switch {
	case w.Phase == "cancel_requested":
		v.NextAction = "wait_cancellation"
	case w.Phase == "complete" || w.Phase == "canceled":
		v.NextAction = "none"
	case w.Phase == "legacy" || w.ObservedStatus == "error" || w.ObservedStatus == "unknown":
		v.NextAction = "contact_support"
	case w.Phase == "documents":
		v.NextAction = "wait_receipt"
		if w.Documents.ManualRequired != nil && *w.Documents.ManualRequired && w.Documents.ManualStatus != "applied" {
			v.NextAction = "submit_receipt"
		}
	case w.ObservedStatus == "draft" && w.Documents.AgreementSigningURL != "":
		v.NextAction = "sign_agreement"
	case w.ObservedStatus == "accepting" && w.Documents.AcceptanceSigningURL != "":
		v.NextAction = "sign_act"
	case w.ObservedStatus == "paid":
		v.NextAction = "wait_financial_details"
	case w.ObservedStatus == "completed" || w.ObservedStatus == "paying":
		v.NextAction = "wait_payment"
	}
	return v, nil
}
