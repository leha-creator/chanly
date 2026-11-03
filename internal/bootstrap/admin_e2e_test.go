package bootstrap

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
)

// Этап «API администратора»: матрица ролей R-1 по контракту. Каждая операция /admin/* в
// api/openapi.yaml объявляет роль; без сессии — 401, пользователь кабинета — 403, moderator на
// операциях «Роль: senior» — 403 (проверка роли идёт до чтения объектов и тела запроса).

type adminOp struct {
	method, path, id string
	senior           bool
	// example — пример тела из контракта: тело проходит проверку по спецификации.
	example any
}

func adminOps(t *testing.T) []adminOp {
	t.Helper()
	spec, err := apigen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var ops []adminOp
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/admin") {
			continue
		}
		for method, op := range item.Operations() {
			desc := op.Description
			if !strings.Contains(desc, "Роль: ") {
				t.Errorf("%s %s (%s): no role in description", method, path, op.OperationID)
				continue
			}
			var example any
			if rb := op.RequestBody; rb != nil && rb.Value != nil {
				if mt := rb.Value.Content.Get("application/json"); mt != nil {
					example = mt.Example
				}
				if rb.Value.Content.Get("multipart/form-data") != nil {
					example = fileBody("statement.txt", []byte("1CClientBankExchange"))
				}
			}
			p := strings.NewReplacer("{id}", uuid.NewString(), "{period}", "2026-09", "{type}", "creative").Replace(path)
			ops = append(ops, adminOp{method: method, path: p, id: op.OperationID, example: example,
				senior: strings.Contains(desc, "Роль: senior")})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].id < ops[j].id })
	return ops
}

func TestAdminRoleMatrixHTTP(t *testing.T) {
	a := newTestApp(t)
	mod := a.adminClient("mod.matrix@example.ru", accountdomain.AdminModerator)
	senior := a.adminClient("senior.matrix@example.ru", accountdomain.AdminSenior)
	adv := a.newAdvertiser("adv.matrix@example.ru")
	anon := a.client()
	body := map[string]any{"reason": "проверка матрицы ролей"}

	ops := adminOps(t)
	seniorOps := 0
	for _, op := range ops {
		if r := anon.doKey(op.method, op.path, "matrix-anon-"+op.id, body); r.status != http.StatusUnauthorized {
			t.Errorf("anonymous %s (%s %s): %d %s", op.id, op.method, op.path, r.status, r.body)
		}
		if r := adv.doKey(op.method, op.path, "matrix-adv-"+op.id, body); r.status != http.StatusForbidden {
			t.Errorf("advertiser %s (%s %s): %d %s", op.id, op.method, op.path, r.status, r.body)
		}
		if !op.senior {
			continue
		}
		seniorOps++
		modBody := any(body)
		if op.example != nil {
			modBody = op.example
		}
		send := func(c *client, b any) response {
			if _, ok := b.(multipartBody); ok {
				return c.do(op.method, op.path, b)
			}
			return c.doKey(op.method, op.path, "matrix-"+op.id, b)
		}
		if r := send(mod, modBody); r.status != http.StatusForbidden {
			t.Errorf("moderator on senior %s (%s %s): %d %s", op.id, op.method, op.path, r.status, r.body)
		}
		// senior проходит проверку роли: дальше — 2xx, 404 (несуществующий объект) или ошибка данных, но не 401/403/501.
		r := senior.doKey(op.method, op.path, "matrix-senior-"+op.id, body)
		if r.status == http.StatusUnauthorized || r.status == http.StatusForbidden || r.status == http.StatusNotImplemented {
			t.Errorf("senior %s (%s %s): %d %s", op.id, op.method, op.path, r.status, r.body)
		}
	}
	if len(ops) < 50 || seniorOps < 25 {
		t.Fatalf("admin operations %d, senior-only %d: contract not read", len(ops), seniorOps)
	}
	// Отказы по роли — в журнале приложения без почты.
	if !strings.Contains(a.logs.String(), "access denied") {
		t.Error("no access denied log")
	}
}
