package bootstrap

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/ord"
)

// Этап «API администратора»: организации и пользователи (R-12 … R-14), ошибки ОРД (S-20, R-17).

type adminOrgView struct {
	ID      string `json:"id"`
	INN     string `json:"inn"`
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Members []struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Status string `json:"status"`
	} `json:"members"`
	OrdCounterparty *struct {
		Status string `json:"status"`
		Type   string `json:"type"`
	} `json:"ord_counterparty"`
}

type ordObjectView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	LastRequest *struct {
		Operation      string `json:"operation"`
		ResponseStatus int    `json:"response_status"`
	} `json:"last_request"`
}

func TestAdminOrganizationsAndOrdHTTP(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	adv := a.advertiserClient("adv.admorg@example.ru")
	a.acceptOffers(adv)
	blg := a.bloggerClient("blogger.admorg@example.ru")
	ch := a.activeChannel(blg, "Канал", 1_000_000)
	o := a.orderOn(adv, ch, "Ошибка")
	a.ord.FailNext(ord.OpContract, http.StatusUnprocessableEntity, "body.date: field required")
	a.approveVersion(o.CurrentPostVersion.ID)
	a.ordSettle()

	// Сессии администраторов — после движения часов.
	mod := a.adminClient("mod.admorg@example.ru", accountdomain.AdminModerator)
	senior := a.adminClient("senior.admorg@example.ru", accountdomain.AdminSenior)
	orgID := a.orgOf("adv.admorg@example.ru")
	var inn string
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT inn FROM organization WHERE id = $1`, orgID).Scan(&inn); err != nil {
		t.Fatal(err)
	}

	// Ошибки ОРД: список с фильтром по виду, объект с последним вызовом, повтор (обе роли).
	var errs page[ordObjectView]
	mod.do(http.MethodGet, "/admin/ord/errors?type=contract", nil).json(t, &errs)
	if len(errs.Items) != 1 || errs.Items[0].Type != "contract" || errs.Items[0].Status != "error" {
		t.Fatalf("ord errors: %+v", errs.Items)
	}
	var none page[ordObjectView]
	mod.do(http.MethodGet, "/admin/ord/errors?type=creative", nil).json(t, &none)
	if len(none.Items) != 0 {
		t.Fatalf("creative errors: %+v", none.Items)
	}
	contract := errs.Items[0].ID
	// ordfake не пишет outbound_request — вызов адаптера, как его сохранил бы клиент ОРД.
	if _, err := a.db.Pool.Exec(t.Context(), `INSERT INTO outbound_request (id, system, operation, attempt, request,
		response_status, response_body, error, object_type, object_id, created_at)
		SELECT $1, 'yandex_ord', 'contract', 1, '{"inn":"77****1234"}', 422, '{"error":"body.date"}', 'validation',
		'contract', c.ord_external_id, now() FROM ord_contract c WHERE c.id = $2`, uuid.New(), contract); err != nil {
		t.Fatal(err)
	}
	var obj ordObjectView
	mod.do(http.MethodGet, "/admin/ord/objects/contract/"+contract, nil).json(t, &obj)
	if obj.LastRequest == nil || obj.LastRequest.ResponseStatus != http.StatusUnprocessableEntity || obj.Error == "" {
		t.Fatalf("ord object: %+v", obj)
	}
	expectStatus(t, mod.do(http.MethodGet, "/admin/ord/objects/contract/"+uuid.NewString(), nil), http.StatusNotFound, "unknown object")
	expectStatus(t, mod.do(http.MethodPost, "/admin/ord/objects/contract/"+contract+"/retry", map[string]string{"reason": " "}),
		http.StatusUnprocessableEntity, "retry without reason")
	r := mod.do(http.MethodPost, "/admin/ord/objects/contract/"+contract+"/retry", map[string]string{"reason": "дата исправлена"})
	expectStatus(t, r, http.StatusOK, "retry")
	r.json(t, &obj)
	if obj.Status != "pending" {
		t.Fatalf("after retry: %+v", obj)
	}
	expectStatus(t, mod.do(http.MethodPost, "/admin/ord/objects/contract/"+contract+"/retry", map[string]string{"reason": "ещё раз"}),
		http.StatusConflict, "retry not in error")

	// Поиск по ИНН и по почте участника; ИНН полный только у senior (R-14). Модератор ищет
	// только по ИНН целиком: по началу скрытая часть подбиралась бы перебором.
	var found page[adminOrgView]
	mod.do(http.MethodGet, "/admin/organizations?query="+inn[:6], nil).json(t, &found)
	if len(found.Items) != 0 {
		t.Fatalf("moderator search by INN prefix: %+v", found.Items)
	}
	senior.do(http.MethodGet, "/admin/organizations?query="+inn[:6], nil).json(t, &found)
	if len(found.Items) != 1 || found.Items[0].ID != orgID {
		t.Fatalf("senior search by INN prefix: %+v", found.Items)
	}
	mod.do(http.MethodGet, "/admin/organizations?query="+inn, nil).json(t, &found)
	if len(found.Items) != 1 || found.Items[0].ID != orgID || found.Items[0].INN == inn || !strings.Contains(found.Items[0].INN, "*") {
		t.Fatalf("moderator search by INN: %+v", found.Items)
	}
	senior.do(http.MethodGet, "/admin/organizations?query=ADV.admorg@example.ru", nil).json(t, &found)
	if len(found.Items) != 1 || found.Items[0].INN != inn {
		t.Fatalf("senior search by email: %+v", found.Items)
	}
	senior.do(http.MethodGet, "/admin/organizations?role=blogger", nil).json(t, &found)
	for _, it := range found.Items {
		if it.ID == orgID {
			t.Fatalf("advertiser listed as blogger: %+v", it)
		}
	}

	// Карточка: участники с маской почты у moderator, контрагент ОРД.
	var card adminOrgView
	mod.do(http.MethodGet, "/admin/organizations/"+orgID, nil).json(t, &card)
	if len(card.Members) != 1 || card.Members[0].Email == "adv.admorg@example.ru" || card.OrdCounterparty == nil ||
		card.OrdCounterparty.Type != "organization" {
		t.Fatalf("moderator card: %+v", card)
	}
	userID := card.Members[0].ID
	senior.do(http.MethodGet, "/admin/organizations/"+orgID, nil).json(t, &card)
	if card.Members[0].Email != "adv.admorg@example.ru" || card.INN != inn {
		t.Fatalf("senior card: %+v", card)
	}

	// Правка реквизитов — только senior; новое наименование уходит в ОРД заново (R-15).
	rename := map[string]string{"name": "ООО «Ромашка Админ»", "reason": "по выписке ЕГРЮЛ"}
	expectStatus(t, mod.do(http.MethodPatch, "/admin/organizations/"+orgID, rename), http.StatusForbidden, "moderator update")
	expectStatus(t, senior.do(http.MethodPatch, "/admin/organizations/"+orgID, map[string]string{"name": "x"}),
		http.StatusUnprocessableEntity, "update without reason")
	r = senior.do(http.MethodPatch, "/admin/organizations/"+orgID, rename)
	expectStatus(t, r, http.StatusOK, "senior update")
	if r.json(t, &card); card.Name != "ООО «Ромашка Админ»" {
		t.Fatalf("updated card: %+v", card)
	}
	a.ordSettle()
	var body ord.Organization
	if obj, ok := a.ord.Object(orgID); !ok || obj.Submits < 2 || obj.Decode(&body) != nil || body.Name != "ООО «Ромашка Админ»" {
		t.Fatalf("counterparty not resubmitted: %+v", body)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'organization.admin_updated' AND reason = 'по выписке ЕГРЮЛ'`); n != 1 {
		t.Fatalf("admin update audit: %d", n)
	}

	// Блокировка: сессия перестаёт работать; администратора — 409 admin_user (R-12).
	reason := map[string]string{"reason": "Мошеннические оплаты"}
	expectStatus(t, mod.do(http.MethodPost, "/admin/users/"+userID+"/block", reason), http.StatusForbidden, "moderator block")
	r = senior.do(http.MethodPost, "/admin/users/"+userID+"/block", reason)
	expectStatus(t, r, http.StatusOK, "block")
	var u struct {
		Status        string `json:"status"`
		BlockedReason string `json:"blocked_reason"`
	}
	if r.json(t, &u); u.Status != "blocked" || u.BlockedReason != "Мошеннические оплаты" {
		t.Fatalf("blocked user: %+v", u)
	}
	expectStatus(t, adv.do(http.MethodGet, "/me", nil), http.StatusUnauthorized, "blocked session")
	var modID string
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT id::text FROM "user" WHERE email = 'mod.admorg@example.ru'`).Scan(&modID); err != nil {
		t.Fatal(err)
	}
	r = senior.do(http.MethodPost, "/admin/users/"+modID+"/block", reason)
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "admin_user" {
		t.Fatalf("block admin: %d %s", r.status, r.body)
	}
	r = senior.do(http.MethodPost, "/admin/users/"+userID+"/unblock", map[string]string{"reason": "Проверка завершена"})
	expectStatus(t, r, http.StatusOK, "unblock")
	u.Status, u.BlockedReason = "", ""
	if r.json(t, &u); u.Status != "active" || u.BlockedReason != "" {
		t.Fatalf("unblocked user: %+v", u)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action IN ('user.blocked', 'user.unblocked') AND actor_type = 'admin'`); n != 2 {
		t.Fatalf("block audit: %d", n)
	}
	if strings.Contains(a.logs.String(), "Мошеннические оплаты") {
		t.Fatal("block reason in application log")
	}

	// Журнал действий: фильтр по действию и периоду (R-15; день — по Москве).
	day := a.clk.Now().In(time.FixedZone("MSK", 3*3600)).Format(time.DateOnly)
	for query, want := range map[string]int{
		"action=user.blocked":                            1,
		"action=user.blocked&from=" + day + "&to=" + day: 1,
		"action=user.blocked&to=2026-10-01":              0,
		"action=user.blocked&from=2026-12-01":            0,
	} {
		var entries page[struct {
			Action string `json:"action"`
		}]
		senior.do(http.MethodGet, "/admin/audit-log?"+query, nil).json(t, &entries)
		if len(entries.Items) != want {
			t.Errorf("audit log %s: %d entries, want %d", query, len(entries.Items), want)
		}
	}
}
