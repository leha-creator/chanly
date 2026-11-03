// Package ordfake — Яндекс ОРД в памяти для тестов и разработки без кабинета ОРД (07 §9).
//
// Объекты хранятся по нашим идентификаторам, повторный POST обновляет объект (R-1). Ответ
// на POST — «Request accepted by ORD»; каждый опрос GET /status продвигает объекты запроса
// на шаг: ORD success → ERIR sync success → ERIR async success. Площадка, статистика и акт
// завершаются на ORD success (R-3, R-22). Креатив получает токен fake-erid-N сразу в ответе.
package ordfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Статусы ОРД.
const (
	statusAccepted    = "Request accepted by ORD"
	statusOrdSuccess  = "ORD success"
	statusSyncSuccess = "ERIR sync success"
	statusDone        = "ERIR async success"
)

// Object — объект в поддельном ОРД.
type Object struct {
	Op     string
	ID     string
	Body   json.RawMessage
	Status string
	Token  string
	// Submits — сколько раз объект отправлялся.
	Submits int
	errors  []string
}

type failure struct {
	status int
	body   string
	// applied — объект сохраняется, но вызов возвращает ошибку (неизвестный исход).
	applied bool
}

// Client — поддельный ОРД. Безопасен для конкурентного доступа.
type Client struct {
	mu        sync.Mutex
	objects   map[string]*Object
	requests  map[string][]string
	calls     map[string]int
	fail      map[string][]failure
	rejects   map[string]rejection
	held      map[string]bool
	tokenLate bool
	reqSeq    int
	tokenSeq  int
	statusErr error
}

type rejection struct {
	status string
	msg    string
}

// New создаёт пустой ОРД.
func New() *Client {
	return &Client{
		objects: map[string]*Object{}, requests: map[string][]string{}, calls: map[string]int{},
		fail: map[string][]failure{}, rejects: map[string]rejection{}, held: map[string]bool{},
	}
}

// UpsertOrganization сохраняет контрагента.
func (c *Client) UpsertOrganization(_ context.Context, o ord.Organization) (ord.Receipt, error) {
	return c.upsert(ord.OpOrganization, []string{o.ID}, o)
}

// UpsertContract сохраняет договор.
func (c *Client) UpsertContract(_ context.Context, ct ord.Contract) (ord.Receipt, error) {
	return c.upsert(ord.OpContract, []string{ct.ID}, ct)
}

// UpsertPlatforms сохраняет площадки.
func (c *Client) UpsertPlatforms(_ context.Context, p ord.Platforms) (ord.Receipt, error) {
	ids := make([]string, 0, len(p.Platforms))
	for _, pl := range p.Platforms {
		ids = append(ids, pl.PlatformID)
	}
	return c.upsert(ord.OpPlatforms, ids, p)
}

// UpsertCreative сохраняет креатив и выдаёт токен.
func (c *Client) UpsertCreative(_ context.Context, cr ord.Creative) (ord.Receipt, error) {
	return c.upsert(ord.OpCreative, []string{cr.ID}, cr)
}

// UpsertStatistics сохраняет статистику месяца.
func (c *Client) UpsertStatistics(_ context.Context, s ord.Statistics) (ord.Receipt, error) {
	return c.upsert(ord.OpStatistics, []string{s.ID}, s)
}

// UpsertInvoice сохраняет акт.
func (c *Client) UpsertInvoice(_ context.Context, inv ord.Invoice) (ord.Receipt, error) {
	return c.upsert(ord.OpInvoice, []string{inv.ID}, inv)
}

// UpsertInvoiceCreatives сохраняет разбивку акта: объект `<id акта>/creatives`.
func (c *Client) UpsertInvoiceCreatives(_ context.Context, ic ord.InvoiceCreatives) (ord.Receipt, error) {
	return c.upsert(ord.OpInvoiceCreatives, []string{InvoiceCreativesID(ic.InvoiceID)}, ic)
}

// InvoiceCreativesID — идентификатор разбивки акта в поддельном ОРД.
func InvoiceCreativesID(invoiceID string) string { return invoiceID + "/creatives" }

func (c *Client) upsert(op string, ids []string, body any) (ord.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[op]++
	f, failing := c.nextFailure(op)
	if failing && !f.applied {
		return ord.Receipt{}, failureError(op, f)
	}
	raw, _ := json.Marshal(body)
	c.reqSeq++
	reqID := fmt.Sprintf("fake-req-%d", c.reqSeq)
	rec := ord.Receipt{RequestID: reqID, Status: statusAccepted}
	for _, id := range ids {
		obj := c.objects[id]
		if obj == nil {
			obj = &Object{Op: op, ID: id}
			c.objects[id] = obj
		}
		obj.Body, obj.Status, obj.errors = raw, statusAccepted, nil
		obj.Submits++
		if op == ord.OpCreative && obj.Token == "" {
			c.tokenSeq++
			obj.Token = fmt.Sprintf("fake-erid-%d", c.tokenSeq)
		}
		st := ord.ObjectStatus{ObjectID: id, Status: obj.Status}
		if !c.tokenLate {
			st.Token = obj.Token
			rec.Token = obj.Token
		}
		rec.ObjectStatuses = append(rec.ObjectStatuses, st)
	}
	c.requests[reqID] = ids
	if failing {
		return ord.Receipt{}, failureError(op, f)
	}
	return rec, nil
}

// GetStatus продвигает объекты запроса на шаг и возвращает их статусы.
func (c *Client) GetStatus(_ context.Context, requestID string) (ord.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[ord.OpStatus]++
	if c.statusErr != nil {
		return ord.Receipt{}, c.statusErr
	}
	ids, ok := c.requests[requestID]
	if !ok {
		return ord.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"query.reqid: unknown request"}})
	}
	rec := ord.Receipt{RequestID: requestID}
	for _, id := range ids {
		obj := c.objects[id]
		c.advance(obj)
		st := ord.ObjectStatus{ObjectID: id, Status: obj.Status, Token: obj.Token}
		for _, e := range obj.errors {
			m, _ := json.Marshal(e)
			st.ErrorMessage = append(st.ErrorMessage, m)
		}
		if obj.Op == ord.OpCreative {
			rec.Token = obj.Token
		}
		rec.ObjectStatuses = append(rec.ObjectStatuses, st)
		if rec.Status == "" {
			rec.Status = obj.Status
		}
	}
	return rec, nil
}

func (c *Client) advance(obj *Object) {
	if r, ok := c.rejects[obj.ID]; ok {
		obj.Status, obj.errors = r.status, []string{r.msg}
		return
	}
	if c.held[obj.ID] {
		return
	}
	switch obj.Status {
	case statusAccepted:
		obj.Status = statusOrdSuccess
	case statusOrdSuccess:
		if !stopsAtOrd(obj.Op) {
			obj.Status = statusSyncSuccess
		}
	case statusSyncSuccess:
		obj.Status = statusDone
	}
}

// stopsAtOrd — объекты без проверки ЕРИР: финальный статус — ORD success.
func stopsAtOrd(op string) bool {
	switch op {
	case ord.OpPlatforms, ord.OpStatistics, ord.OpInvoice, ord.OpInvoiceCreatives:
		return true
	}
	return false
}

func (c *Client) nextFailure(op string) (failure, bool) {
	q := c.fail[op]
	if len(q) == 0 {
		return failure{}, false
	}
	c.fail[op] = q[1:]
	return q[0], true
}

// failureError — ошибка в том виде, в каком её возвращает настоящий адаптер.
func failureError(op string, f failure) error {
	switch {
	case f.status == http.StatusUnprocessableEntity:
		return job.Permanent(&ord.ValidationError{Details: []string{f.body}})
	case f.status >= 400 && f.status < 500 && f.status != http.StatusTooManyRequests:
		return job.Permanent(fmt.Errorf("yandex_ord ORD.%s rejected with status %d: %s", op, f.status, f.body))
	}
	return fmt.Errorf("yandex_ord ORD.%s: status %d: %s", op, f.status, f.body)
}

// FailNext — следующий вызов op завершится ответом status с телом body (для 422 — текст
// ошибки проверки). Вызовы накапливаются очередью.
func (c *Client) FailNext(op string, status int, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail[op] = append(c.fail[op], failure{status: status, body: body})
}

// TimeoutNext — следующий вызов op сохранит объект, но вернёт повторяемую ошибку:
// неизвестный исход (05 §1).
func (c *Client) TimeoutNext(op string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail[op] = append(c.fail[op], failure{status: http.StatusGatewayTimeout, body: "timeout", applied: true})
}

// RejectObject — при следующем опросе объект id получит финальную ошибку ordStatus.
func (c *Client) RejectObject(id, ordStatus, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejects[id] = rejection{status: ordStatus, msg: msg}
}

// ClearReject снимает RejectObject.
func (c *Client) ClearReject(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rejects, id)
}

// TokenOnlyInStatus — токен креатива не приходит в ответе на POST, только при опросе.
func (c *Client) TokenOnlyInStatus() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokenLate = true
}

// HoldStatus — статус объекта id при опросе не меняется (false — снова меняется).
func (c *Client) HoldStatus(id string, hold bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held[id] = hold
}

// FailStatus — опрос статуса возвращает err (nil — снова работает): ошибка сети.
func (c *Client) FailStatus(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusErr = err
}

// Calls — сколько раз вызвана операция op (organization, contract, platforms, creative, status).
func (c *Client) Calls(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[op]
}

// Objects — копии объектов по идентификатору.
func (c *Client) Objects() map[string]Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Object, len(c.objects))
	for id, o := range c.objects {
		out[id] = *o
	}
	return out
}

// Object — копия объекта id; false — объекта нет.
func (c *Client) Object(id string) (Object, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.objects[id]
	if !ok {
		return Object{}, false
	}
	return *o, true
}

// Decode разбирает тело объекта в v.
func (o Object) Decode(v any) error {
	if len(o.Body) == 0 {
		return errors.New("ordfake: empty body")
	}
	return json.Unmarshal(o.Body, v)
}
