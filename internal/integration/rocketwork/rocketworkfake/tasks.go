package rocketworkfake

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func cloneTask(t rocketwork.TaskSnapshot) rocketwork.TaskSnapshot {
	b, _ := json.Marshal(t)
	var out rocketwork.TaskSnapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func (f *Fake) LoseTaskResponse(action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loseTaskWrites == nil {
		f.loseTaskWrites = map[string]bool{}
	}
	f.loseTaskWrites[action] = true
}
func (f *Fake) HideTaskLookups(n int) { f.mu.Lock(); defer f.mu.Unlock(); f.hiddenTaskLookups = n }

func (f *Fake) CreateTask(ctx context.Context, t rocketwork.TaskTerms) (rocketwork.TaskSnapshot, error) {
	var out rocketwork.TaskSnapshot
	lost := false
	err := f.with(func(st *state) error {
		f.record(ctx, "CreateTask", t.ExternalID)
		if err := f.injected("CreateTask"); err != nil {
			return err
		}
		if t.AmountKopecks <= 0 || t.ExternalID == "" || t.ExecutorINN == "" || t.Title == "" || t.Description == "" {
			return rocketwork.ErrFormat
		}
		if st.Tasks == nil {
			st.Tasks = map[int64]rocketwork.TaskSnapshot{}
		}
		for _, task := range st.Tasks {
			if task.ExternalID == t.ExternalID {
				out = cloneTask(task)
				return nil
			}
		}
		st.Seq++
		signed, act, tax := true, t.ActRequired, t.TaxRetention
		amount := t.AmountKopecks
		out = rocketwork.TaskSnapshot{ID: int64(st.Seq), Type: "OneTime", ExternalID: t.ExternalID, ExecutorINN: t.ExecutorINN, ExecutorID: 7, EmploymentKind: t.EmploymentKind, AmountKopecks: amount, ExecutorAmountKopecks: &amount, Status: "inwork", SignatureForm: t.SignatureForm, ActRequired: &act, TaxRetention: &tax, CustomerSigned: &signed, ExecutorSigned: &signed}
		for _, c := range st.Contractors {
			if c.INN == t.ExecutorINN {
				n, _ := strconv.ParseInt(strings.TrimPrefix(c.ID, "rwc-"), 10, 64)
				out.ExecutorID = n
				if c.Status != "active" {
					return rocketwork.ErrFormat
				}
			}
		}
		st.Tasks[out.ID] = cloneTask(out)
		lost = f.loseTaskWrites["create"]
		delete(f.loseTaskWrites, "create")
		return nil
	})
	if err == nil && lost {
		return rocketwork.TaskSnapshot{}, rocketwork.ErrUncertain
	}
	return out, err
}
func (f *Fake) GetTask(ctx context.Context, id int64) (rocketwork.TaskSnapshot, error) {
	var out rocketwork.TaskSnapshot
	err := f.with(func(st *state) error {
		f.record(ctx, "GetTask", strconv.FormatInt(id, 10))
		if err := f.injected("GetTask"); err != nil {
			return err
		}
		t, ok := st.Tasks[id]
		if !ok {
			return rocketwork.ErrNotFound
		}
		out = cloneTask(t)
		return nil
	})
	return out, err
}
func (f *Fake) FindTask(ctx context.Context, external string) (rocketwork.TaskSnapshot, bool, error) {
	var out rocketwork.TaskSnapshot
	found := false
	err := f.with(func(st *state) error {
		f.record(ctx, "FindTask", external)
		if f.hiddenTaskLookups > 0 {
			f.hiddenTaskLookups--
			return nil
		}
		for _, t := range st.Tasks {
			if t.ExternalID == external {
				if found {
					return rocketwork.ErrFormat
				}
				out = cloneTask(t)
				found = true
			}
		}
		return nil
	})
	return out, found, err
}
func (f *Fake) taskAction(ctx context.Context, id int64, action string) (rocketwork.TaskSnapshot, error) {
	var out rocketwork.TaskSnapshot
	lost := false
	err := f.with(func(st *state) error {
		f.record(ctx, "Task."+action, strconv.FormatInt(id, 10))
		if err := f.injected("Task." + action); err != nil {
			return err
		}
		t, ok := st.Tasks[id]
		if !ok {
			return rocketwork.ErrNotFound
		}
		next := ""
		switch action {
		case "complete":
			if t.Status == "inwork" || t.Status == "accepting" {
				next = "completed"
				if t.ActRequired != nil && *t.ActRequired {
					next = "accepting"
				}
			}
		case "sign_act":
			if t.Status == "accepting" && t.ActRequired != nil && *t.ActRequired {
				next = "completed"
			}
		case "pay":
			if t.Status == "completed" {
				next = "paying"
			}
		case "cancel":
			if (t.Status == "draft" || t.Status == "inwork") && t.CustomerSigned != nil && !*t.CustomerSigned && t.ExecutorSigned != nil && !*t.ExecutorSigned {
				next = "canceled"
			}
		}
		if next == "" {
			return rocketwork.ErrFormat
		}
		t.Status = next
		st.Tasks[id] = t
		out = cloneTask(t)
		lost = f.loseTaskWrites[action]
		delete(f.loseTaskWrites, action)
		return nil
	})
	if err == nil && lost {
		return rocketwork.TaskSnapshot{}, rocketwork.ErrUncertain
	}
	return out, err
}
func (f *Fake) CompleteTask(ctx context.Context, id int64) (rocketwork.TaskSnapshot, error) {
	return f.taskAction(ctx, id, "complete")
}
func (f *Fake) SignTaskAct(ctx context.Context, id int64) (rocketwork.TaskSnapshot, error) {
	return f.taskAction(ctx, id, "sign_act")
}
func (f *Fake) PayTask(ctx context.Context, id int64) (rocketwork.TaskSnapshot, error) {
	return f.taskAction(ctx, id, "pay")
}
func (f *Fake) CancelTask(ctx context.Context, id int64) (rocketwork.TaskSnapshot, error) {
	return f.taskAction(ctx, id, "cancel")
}

// ObserveTask lets tests independently advance asynchronous payment/documents.
func (f *Fake) ObserveTask(id int64, status string, fee *int64, receipt string) error {
	return f.with(func(st *state) error {
		t, ok := st.Tasks[id]
		if !ok {
			return rocketwork.ErrNotFound
		}
		t.Status, t.CommissionKopecks, t.ReceiptURL = status, fee, receipt
		st.Tasks[id] = cloneTask(t)
		return nil
	})
}

func (f *Fake) LookupExecutor(ctx context.Context, r rocketwork.ExecutorIdentity) (rocketwork.Executor, error) {
	var out rocketwork.Executor
	err := f.with(func(st *state) error {
		for _, c := range st.Contractors {
			if r.Kind == "inn" && c.INN == r.Value {
				n, _ := strconv.ParseInt(strings.TrimPrefix(c.ID, "rwc-"), 10, 64)
				if r.ExpectedID != 0 && r.ExpectedID != n {
					return rocketwork.ErrFormat
				}
				out = rocketwork.Executor{ID: n, INN: c.INN, Phone: r.Phone, ExternalID: c.ExternalID, EmploymentKind: r.EmploymentKind, Known: true, Status: c.Status}
				if c.Status != "active" {
					out.Status = rocketwork.ContractorPending
					out.Reason = "incomplete_profile"
					out.NextAction = "complete_registration"
				}
				return nil
			}
		}
		return rocketwork.ErrNotFound
	})
	return out, err
}
