package rocketwork

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
)

// WithdrawalTerms contains only the platform's approved bank destination.
// POST may immediately initiate payment: it is itself the financial action.
type WithdrawalTerms struct {
	ExternalID, CompanyINN, BIC, AccountNumber string
	ExecutorID                                 int64
	AmountKopecks, MaxCommissionKopecks        int64
}

func digits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *Client) CreateWithdrawal(ctx context.Context, t WithdrawalTerms) (TaskSnapshot, error) {
	if t.ExternalID == "" || t.ExecutorID <= 0 || (!digits(t.CompanyINN, 10) && !digits(t.CompanyINN, 12)) || !digits(t.BIC, 9) || !digits(t.AccountNumber, 20) || t.AmountKopecks <= 0 || t.MaxCommissionKopecks < 0 || t.AmountKopecks > math.MaxInt64-t.MaxCommissionKopecks {
		return TaskSnapshot{}, ErrFormat
	}
	var account accountDTO
	if err := c.read(ctx, "WithdrawalAccount", "/api/account", nil, &account); err != nil {
		return TaskSnapshot{}, err
	}
	balance, err := rublesToKopecks(string(account.Balance))
	if err != nil || account.CompanyINN != t.CompanyINN || balance < t.AmountKopecks+t.MaxCommissionKopecks {
		return TaskSnapshot{}, ErrFormat
	}
	var executor struct {
		Executor *executorDTO `json:"executor"`
	}
	if err := c.read(ctx, "WithdrawalExecutor", "/api/executors/"+url.PathEscape(t.CompanyINN), nil, &executor); err != nil {
		return TaskSnapshot{}, err
	}
	if executor.Executor == nil || executor.Executor.ID != t.ExecutorID || executor.Executor.INN != t.CompanyINN || executor.Executor.Type != "Withdrawal" || executor.Executor.EmploymentKind != "self" {
		return TaskSnapshot{}, ErrFormat
	}
	request := struct {
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Type        string         `json:"type"`
		ExternalID  string         `json:"external_id"`
		INN         string         `json:"executor_inn"`
		Amount      json.Number    `json:"amount_gross"`
		PaymentInfo map[string]any `json:"payment_info"`
	}{"Вывод собственных средств", "Перевод на собственный банковский счёт платформы", "Withdrawal", t.ExternalID, t.CompanyINN, kopecksJSON(t.AmountKopecks), map[string]any{"bank_account": map[string]string{"bic": t.BIC, "account_number": t.AccountNumber}}}
	var response struct {
		Task *taskDTO `json:"task"`
	}
	if err := c.write(ctx, "CreateWithdrawal", http.MethodPost, "/api/tasks", request, &response); err != nil {
		return TaskSnapshot{}, err
	}
	if response.Task == nil {
		return TaskSnapshot{}, ErrUncertain
	}
	s, err := response.Task.snapshot()
	if err != nil || s.Type != "Withdrawal" || s.ExternalID != t.ExternalID || s.AmountKopecks != t.AmountKopecks || s.ExecutorINN != t.CompanyINN || s.ExecutorID != t.ExecutorID || s.EmploymentKind != "self" {
		return TaskSnapshot{}, ErrUncertain
	}
	return s, nil
}
