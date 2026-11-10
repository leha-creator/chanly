package rocketworktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Emulator implements wire shapes independently of production DTOs. All
// successful responses and write bodies pass the pinned OpenAPI validator.
type Emulator struct {
	mu              sync.Mutex
	t               testing.TB
	validate        func(string, any) error
	Token           string
	executor        map[string]any
	tasks           map[int64]map[string]any
	subscriptions   []map[string]any
	events          []map[string]any
	writes          map[string]int
	lose            map[string]bool
	fail            map[string]int
	hidden          int
	duplicate       bool
	seq             int64
	executorMissing bool
	accountINN      string
}

func New(t testing.TB) *Emulator {
	return &Emulator{t: t, validate: Validator(t), Token: "synthetic-emulator-token", tasks: map[int64]map[string]any{}, subscriptions: []map[string]any{}, events: []map[string]any{}, writes: map[string]int{}, lose: map[string]bool{}, fail: map[string]int{}, executor: map[string]any{"id": 7, "type": "Individual", "phone": "+79000000000", "inn": "000000000000", "first_name": "Synthetic", "last_name": "Fixture", "is_resident": true, "document_kind": "russian", "employment_kind": "selfemployed", "payment_readiness": "all_is_well", "selfemployed_status": "validated", "selfemployed_profiles": []any{map[string]any{"provider": "rocketwork", "selfemployed_status": "validated"}}}}
}
func copyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var c map[string]any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	_ = d.Decode(&c)
	return c
}
func (e *Emulator) ExecutorMissing(v bool) { e.mu.Lock(); defer e.mu.Unlock(); e.executorMissing = v }
func (e *Emulator) ConfigureWithdrawal(inn string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.accountINN = inn
	e.executor["inn"], e.executor["type"], e.executor["employment_kind"] = inn, "Withdrawal", "self"
}
func schemaPointer(schema string) string {
	if strings.HasPrefix(schema, "/") {
		return schema
	}
	return "/components/schemas/" + schema
}
func (e *Emulator) ConfigureExecutor(inn, phone, employment, readiness string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executor["inn"], e.executor["phone"], e.executor["employment_kind"], e.executor["payment_readiness"] = inn, phone, employment, readiness
}
func (e *Emulator) LoseNext(method, path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lose[method+" "+path] = true
}
func (e *Emulator) FailNext(method, path string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fail[method+" "+path] = status
}
func (e *Emulator) HideLookups(n int)      { e.mu.Lock(); defer e.mu.Unlock(); e.hidden = n }
func (e *Emulator) DuplicateLookup(v bool) { e.mu.Lock(); defer e.mu.Unlock(); e.duplicate = v }
func (e *Emulator) WriteCount(method, path string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writes[method+" "+path]
}
func (e *Emulator) Task(id int64) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return copyMap(e.tasks[id])
}
func (e *Emulator) SetTask(id int64, fields map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range fields {
		e.tasks[id][k] = v
	}
}
func (e *Emulator) Event(id int64, status string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	row := map[string]any{"id": e.seq, "event": "task." + status, "data": copyMap(e.tasks[id]), "happened_at": time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), "status": "created"}
	e.events = append(e.events, row)
	out := copyMap(row)
	delete(out, "status")
	b, _ := json.Marshal(out)
	return b
}
func (e *Emulator) response(w http.ResponseWriter, status int, schema string, body any) {
	if schema != "" {
		if err := e.validate(schemaPointer(schema), body); err != nil {
			e.t.Errorf("emulator response %s violates contract: %v", schema, err)
			http.Error(w, "invalid fixture", 500)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func (e *Emulator) body(w http.ResponseWriter, r *http.Request, schema string) (map[string]any, bool) {
	var body map[string]any
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.UseNumber()
	if d.Decode(&body) != nil {
		http.Error(w, "invalid JSON", 400)
		return nil, false
	}
	if err := e.validate(schemaPointer(schema), body); err != nil {
		http.Error(w, "contract violation", 422)
		return nil, false
	}
	return body, true
}
func (e *Emulator) lost(w http.ResponseWriter, key string) bool {
	if !e.lose[key] {
		return false
	}
	delete(e.lose, key)
	if h, ok := w.(http.Hijacker); ok {
		c, _, err := h.Hijack()
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	http.Error(w, "response lost", 503)
	return true
}

func (e *Emulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+e.Token {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Header.Get("Idempotency-Key") != "" {
		http.Error(w, "undocumented header", 422)
		return
	}
	key := r.Method + " " + r.URL.Path
	if r.Method != "GET" {
		e.writes[key]++
	}
	if code := e.fail[key]; code != 0 {
		delete(e.fail, key)
		http.Error(w, "synthetic failure", code)
		return
	}
	switch {
	case key == "GET /api/ping":
		e.response(w, 200, "/paths/~1api~1ping/get/responses/200/content/application~1json/schema", map[string]any{"message": "Synthetic greeting"})
	case key == "GET /api/account":
		inn := e.accountINN
		if inn == "" {
			inn = "000000000000"
		}
		e.response(w, 200, "AccountDetails", map[string]any{"balance": json.Number("1000000.00"), "company_inn": inn, "company_name": "Synthetic platform", "requisites": map[string]any{}})
	case strings.HasPrefix(key, "GET /api/executors/"):
		if e.executorMissing {
			http.Error(w, "not found", 404)
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/api/executors/") != e.executor["inn"] && strings.TrimPrefix(r.URL.Path, "/api/executors/") != e.executor["phone"] {
			http.Error(w, "not found", 404)
			return
		}
		e.response(w, 200, "ExecutorWrap", map[string]any{"executor": e.executor})
	case key == "GET /api/executors":
		e.response(w, 200, "Executors", map[string]any{"executors": []any{e.executor}, "meta": map[string]any{"current": 1, "last": 1, "total": 1}})
	case key == "POST /api/executors/invite":
		_, ok := e.body(w, r, "/paths/~1api~1executors~1invite/post/requestBody/content/application~1json/schema")
		if !ok {
			return
		}
		if e.lost(w, key) {
			return
		}
		e.response(w, 200, "/paths/~1api~1executors~1invite/post/responses/200/content/application~1json/schema", map[string]any{"status": "invitation_sent"})
	case key == "POST /api/executors/rebind":
		body, ok := e.body(w, r, "/paths/~1api~1executors~1rebind/post/requestBody/content/application~1json/schema")
		if !ok {
			return
		}
		if body["inn"] != e.executor["inn"] {
			http.Error(w, "unknown identity", 422)
			return
		}
		if e.lost(w, key) {
			return
		}
		e.response(w, 200, "ExecutorWrap", map[string]any{"executor": e.executor})
	case key == "POST /api/executors":
		body, ok := e.body(w, r, "ExecutorCreateParams")
		if !ok {
			return
		}
		fields, ok := body["executor"].(map[string]any)
		if !ok || e.validate("/components/schemas/ExecutorCreateParams/anyOf/8", body) != nil {
			http.Error(w, "entrepreneur required", 422)
			return
		}
		for k, v := range fields {
			e.executor[k] = v
		}
		e.executorMissing = false
		if e.lost(w, key) {
			return
		}
		e.response(w, 200, "ExecutorWrap", map[string]any{"executor": e.executor})
	case key == "POST /api/tasks":
		body, ok := e.body(w, r, "TaskCreateParams")
		if !ok {
			return
		}
		for _, task := range e.tasks {
			if task["external_id"] == body["external_id"] {
				e.response(w, 200, "Task", map[string]any{"task": task})
				return
			}
		}
		if body["executor_inn"] != e.executor["inn"] || e.executor["payment_readiness"] != "all_is_well" {
			http.Error(w, "executor not ready", 422)
			return
		}
		if body["type"] != "OneTime" && body["type"] != "Withdrawal" {
			http.Error(w, "scenario does not permit this task type", 422)
			return
		}
		if body["type"] == "OneTime" {
			for _, flag := range []string{"autopayable", "autosignable", "tax_retention", "act_required"} {
				if _, ok := body[flag].(bool); !ok {
					http.Error(w, "explicit policy required", 422)
					return
				}
			}
		} else if e.executor["type"] != "Withdrawal" || body["payment_info"] == nil || body["executor_inn"] != e.accountINN {
			http.Error(w, "own account required", 422)
			return
		}
		e.seq++
		id := e.seq
		form := "online"
		if body["document_number"] != nil && body["document_date"] != nil {
			form = "manual"
		}
		task := map[string]any{"id": id, "type": "OneTime", "external_id": body["external_id"], "status": "inwork", "amount_gross": fmt.Sprint(body["amount_gross"]), "executor_amount_gross": fmt.Sprint(body["amount_gross"]), "current_employment_kind": body["current_employment_kind"], "executor": copyMap(e.executor), "signature_form": form, "tax_retention": body["tax_retention"], "act_required": body["act_required"], "signed_at": "2026-11-09T00:00:00Z", "executor_signed_at": "2026-11-09T00:00:00Z", "receipt_uri": nil, "receipts_links": map[string]any{}}
		if body["type"] == "Withdrawal" {
			task["type"], task["status"], task["current_employment_kind"], task["signature_form"], task["tax_retention"], task["act_required"] = "Withdrawal", "paying", "self", "manual", false, false
		}
		e.tasks[id] = task
		if e.lost(w, key) {
			return
		}
		e.response(w, 200, "Task", map[string]any{"task": task})
	case key == "GET /api/tasks":
		items := []any{}
		if e.hidden > 0 {
			e.hidden--
		} else {
			for _, task := range e.tasks {
				if task["external_id"] == r.URL.Query().Get("external_ids[]") {
					items = append(items, task)
					if e.duplicate {
						items = append(items, task)
					}
				}
			}
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		total := len(items)
		last := total
		if last == 0 {
			last = 1
		}
		start := page - 1
		if start < len(items) {
			items = items[start : start+1]
		} else {
			items = []any{}
		}
		e.response(w, 200, "Tasks", map[string]any{"tasks": items, "meta": map[string]any{"current": page, "last": last, "total": total}})
	case strings.HasPrefix(r.URL.Path, "/api/tasks/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		task, ok := e.tasks[id]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		if r.Method == "GET" && len(parts) == 1 {
			e.response(w, 200, "Task", map[string]any{"task": task})
			return
		}
		if r.Method != "PATCH" || len(parts) != 2 {
			http.Error(w, "not found", 404)
			return
		}
		if r.ContentLength > 0 {
			http.Error(w, "body not permitted", 422)
			return
		}
		status := task["status"]
		next := ""
		switch parts[1] {
		case "complete":
			if status == "inwork" || status == "accepting" {
				next = "completed"
				if task["act_required"] == true {
					next = "accepting"
				}
			}
		case "sign_act":
			if status == "accepting" && task["act_required"] == true {
				next = "completed"
			}
		case "pay":
			if status == "completed" {
				next = "paying"
			}
		case "cancel":
			if (status == "draft" || status == "inwork") && task["signed_at"] == nil && task["executor_signed_at"] == nil {
				next = "canceled"
			}
		}
		if next == "" {
			http.Error(w, "invalid transition", 422)
			return
		}
		task["status"] = next
		if e.lost(w, key) {
			return
		}
		e.response(w, 200, "Task", map[string]any{"task": task})
	case key == "GET /api/postback_subscriptions":
		e.response(w, 200, "PostbackSubscriptions", e.subscriptions)
	case key == "POST /api/postback_subscriptions":
		body, ok := e.body(w, r, "PostbackSubscriptionCreateParams")
		if !ok {
			return
		}
		if _, ok := body["version"]; ok {
			http.Error(w, "undocumented version", 422)
			return
		}
		e.seq++
		body["id"], body["version"] = e.seq, "v2"
		e.subscriptions = append(e.subscriptions, body)
		if e.lost(w, key) {
			return
		}
		e.response(w, 201, "PostbackSubscription", body)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/postback_subscriptions/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/postback_subscriptions/"), 10, 64)
		for i, sub := range e.subscriptions {
			if sub["id"] == id {
				e.subscriptions = append(e.subscriptions[:i], e.subscriptions[i+1:]...)
				if e.lost(w, key) {
					return
				}
				e.response(w, 200, "PostbackSubscription", sub)
				return
			}
		}
		http.Error(w, "not found", 404)
	case key == "GET /api/postbacks":
		page, _ := strconv.Atoi(r.URL.Query().Get("page[page]"))
		if page < 1 {
			page = 1
		}
		items := []any{}
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("filter[happened_at_gte]"))
		through, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("filter[happened_at_lte]"))
		for _, event := range e.events {
			at, _ := time.Parse(time.RFC3339Nano, event["happened_at"].(string))
			if !at.Before(from) && !at.After(through) {
				items = append(items, event)
			}
		}
		total := len(items)
		last := total
		if last == 0 {
			last = 1
		}
		if page <= total {
			items = items[page-1 : page]
		} else {
			items = []any{}
		}
		e.response(w, 200, "Postbacks", map[string]any{"postbacks": items, "meta": map[string]any{"current": page, "last": last, "total": total}})
	default:
		http.Error(w, "route not implemented in scenario", 404)
	}
}
