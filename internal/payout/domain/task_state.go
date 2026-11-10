package domain

import "fmt"

func (w Workflow) ValidateTask(t ProviderTask, b ExecutorBinding) error {
	if t.ID <= 0 || (w.TaskID != nil && *w.TaskID != t.ID) || t.ExternalID != w.ExternalID || t.Type != "OneTime" || t.Amount != w.Terms.Amount || t.ExecutorID != w.Terms.ExecutorID || t.ExecutorID != b.ExecutorID || t.EmploymentKind != w.Terms.EmploymentKind || t.ExecutorINN != b.LookupValue || b.LookupKind != "inn" {
		return fmt.Errorf("%w: provider task identity or amount mismatch", ErrInvalidPayout)
	}
	if t.TaxRetention == nil || *t.TaxRetention != w.Terms.TaxRetention || t.ActRequired == nil || *t.ActRequired != w.Terms.ActRequired || t.SignatureForm != w.Terms.SignatureForm {
		return fmt.Errorf("%w: provider agreement policy mismatch", ErrInvalidPayout)
	}
	return nil
}

func OperationObserved(action, status string) bool {
	switch action {
	case "create":
		return status != ""
	case "complete":
		return status == "accepting" || status == "completed" || status == "paying" || status == "paid"
	case "sign_act":
		return status == "completed" || status == "paying" || status == "paid"
	case "pay":
		return status == "paying" || status == "paid"
	case "cancel":
		return status == "canceled"
	}
	return false
}

func NextTaskAction(t ProviderTask) string {
	switch t.Status {
	case "inwork":
		return "complete"
	case "accepting":
		if t.ActRequired != nil && *t.ActRequired {
			return "sign_act"
		}
		return "complete"
	case "completed":
		return "pay"
	}
	return ""
}
