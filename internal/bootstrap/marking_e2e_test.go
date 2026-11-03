package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// acceptOffers — пользователь принимает все действующие документы своей организации.
func (a *testApp) acceptOffers(c *client) {
	a.t.Helper()
	me := c.me(a.t)
	if len(me.Pending) == 0 {
		return
	}
	if r := c.do(http.MethodPost, "/legal-acceptances", map[string]any{"legal_document_ids": me.Pending}); r.status != http.StatusNoContent {
		a.t.Fatalf("accept: %d %s", r.status, r.body)
	}
}

// devApprove — служебный метод maxim dev post-version approve (R-23).
func (a *testApp) devApprove(number int64) string {
	a.t.Helper()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	if err := runDev(context.Background(), cfg, a.mods.in.log,
		[]string{"post-version", "--order", strconv.FormatInt(number, 10), "approve"}, &out); err != nil {
		a.t.Fatalf("dev approve: %v", err)
	}
	a.toTestClock()
	return out.String()
}

// toTestClock — служебные команды работают на системных часах: их задания ОРД переносятся на часы теста.
func (a *testApp) toTestClock() {
	a.t.Helper()
	if _, err := a.db.Pool.Exec(context.Background(), `UPDATE job SET run_at = $1 WHERE status = 'pending' AND run_at > $1
		AND kind IN ('sync_ord_object', 'poll_ord_status')`, a.clk.Now()); err != nil {
		a.t.Fatal(err)
	}
}

// approveVersion — модератор одобряет версию (S-17) через сервис post на часах теста.
func (a *testApp) approveVersion(versionID string) {
	a.t.Helper()
	id := uuid.MustParse(versionID)
	v, err := a.mods.post.Versions.Get(context.Background(), id)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.mods.post.ApproveVersion(context.Background(), id, v.CreatedBy); err != nil {
		a.t.Fatal(err)
	}
}

// untilErid — worker ведёт маркировку, пока версия не получит erid (не дольше steps минут).
func (a *testApp) untilErid(versionID string, steps int) string {
	a.t.Helper()
	for range steps {
		a.settle()
		var erid *string
		if err := a.db.Pool.QueryRow(context.Background(), `SELECT erid FROM creative WHERE post_version_id = $1`, versionID).Scan(&erid); err == nil && erid != nil {
			return *erid
		}
		a.clk.Advance(time.Minute)
	}
	a.t.Fatalf("version %s got no erid", versionID)
	return ""
}

// orderOn — заказ рекламодателя на канал с текстовым постом.
func (a *testApp) orderOn(adv *client, channel, campaignName string) orderView {
	a.t.Helper()
	campaign := a.campaignWithPost(adv, campaignName)
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": channel, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		a.t.Fatalf("cart: %d %s", r.status, r.body)
	}
	var o orderView
	if r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil); r.status != http.StatusCreated {
		a.t.Fatalf("checkout: %d %s", r.status, r.body)
	} else {
		r.json(a.t, &o)
	}
	return o
}

func (a *testApp) alertRaised(reason string) bool {
	return a.count(`SELECT count(*) FROM alert WHERE reason = $1`, reason) > 0
}

func TestMarkingEndToEnd(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	mod := a.adminClient("moderator.ord@example.ru", "moderator")
	adv := a.advertiserClient("adv.ord@example.ru")
	a.acceptOffers(adv)
	blg := a.bloggerClient("blogger.ord@example.ru")
	a.acceptOffers(blg)
	var advOrg struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		INN  string `json:"inn"`
	}
	adv.do(http.MethodGet, "/organizations/current", nil).json(t, &advOrg)

	// S-16: одобрение канала → контрагент блогера, площадка, договор с блогером; в каталоге — после площадки.
	ch := a.moderatedChannel(blg, -73000000001, 5000)
	if r := mod.do(http.MethodPost, "/admin/channels/"+ch+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve channel: %d %s", r.status, r.body)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels/"+ch, nil); r.status != http.StatusNotFound {
		t.Fatalf("card before ord platform: %d", r.status)
	}
	a.ordSettle()
	if r := adv.do(http.MethodGet, "/catalog/channels/"+ch, nil); r.status != http.StatusOK {
		t.Fatalf("card after ord platform: %d", r.status)
	}
	bloggerOrg := blg.orgID(t).String()
	var bloggerBody ord.Organization
	if o, ok := a.ord.Object(bloggerOrg); !ok || o.Decode(&bloggerBody) != nil || !bloggerBody.IsRr {
		t.Fatalf("blogger contractor: %+v", bloggerBody)
	}
	if _, ok := a.ord.Object(ch); !ok || a.ord.Calls(ord.OpContract) != 1 {
		t.Fatalf("platform or publisher contract not registered: contracts %d", a.ord.Calls(ord.OpContract))
	}

	// S-17: заказ → одобрение версии служебным методом → маркировка в worker → erid, P-02.
	o := a.orderOn(adv, ch, "Осень")
	if out := a.devApprove(o.Number); !strings.Contains(out, "status marking") || !strings.Contains(out, "marking: the worker") {
		t.Fatalf("dev approve output %q", out)
	}
	erid := a.untilErid(o.CurrentPostVersion.ID, 15)
	if !strings.HasPrefix(erid, "fake-erid-") {
		t.Fatalf("erid %q", erid)
	}
	o = adv.order(t, o.ID)
	if o.Status != "in_progress" || o.CurrentPostVersion.Status != "ready" || o.Placements[0].Status != "awaiting_blogger" {
		t.Fatalf("order after erid: %+v", o)
	}
	pid := o.Placements[0].ID
	if p := blg.bloggerPlacements(t)[pid]; p.Post == nil || p.Post.Erid != erid || !strings.Contains(p.Post.FinalText, erid) {
		t.Fatalf("blogger sees post %+v", p.Post)
	}
	for range 6 {
		a.clk.Advance(time.Minute)
		a.settle()
	}
	if n := a.count(`SELECT count(*) FROM (SELECT sync_status FROM ord_organization UNION ALL SELECT sync_status FROM ord_contract
		UNION ALL SELECT sync_status FROM ord_platform UNION ALL SELECT sync_status FROM creative) s WHERE sync_status <> 'synced'`); n != 0 {
		t.Fatalf("%d ORD objects not synced", n)
	}

	// Автопостинг: «Опубликовать сейчас» — в канале пост с erid из ОРД (I-01).
	if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	a.clk.Set(at(11, 30, 0))
	a.schedule(blg, pid, map[string]any{"mode": "now"})
	a.clk.Set(at(11, 30, 4))
	a.settle()
	posts := a.maxFake().Posts()
	if len(posts) != 1 || !strings.Contains(posts[0].Text, erid) {
		t.Fatalf("published posts %+v", posts)
	}

	// R-15: новое наименование рекламодателя уходит в ОРД с тем же id.
	if r := adv.do(http.MethodPatch, "/organizations/current", map[string]string{"name": "ООО «Ромашка Плюс»"}); r.status != http.StatusOK {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	a.settle()
	var advBody ord.Organization
	if obj, ok := a.ord.Object(advOrg.ID); !ok || obj.Submits != 2 || obj.Decode(&advBody) != nil || advBody.Name != "ООО «Ромашка Плюс»" {
		t.Fatalf("advertiser resubmit: %+v", advBody)
	}

	a.checkMarkingLog(t, []string{advOrg.INN, advOrg.Name, "ООО «Ромашка Плюс»", "Скидки", "Анна Блогерова"})
}

func TestMarkingErrorsEndToEnd(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	adv := a.advertiserClient("adv.orderr@example.ru")
	a.acceptOffers(adv)
	blg := a.bloggerClient("blogger.orderr@example.ru")
	ch := a.activeChannel(blg, "Канал", 1_000_000)
	ctx := context.Background()

	// S-28 шаг 1: ОРД отклоняет договор — заказ стоит в marking, ошибка в списке, оповещение.
	o := a.orderOn(adv, ch, "Ошибка")
	a.ord.FailNext(ord.OpContract, http.StatusUnprocessableEntity, "body.date: field required")
	a.approveVersion(o.CurrentPostVersion.ID)
	a.ordSettle()
	if o = adv.order(t, o.ID); o.Status != "marking" || o.Placements[0].Status != "created" {
		t.Fatalf("order after ORD error: %s %s", o.Status, o.Placements[0].Status)
	}
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"ord", "errors"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "contract:") || !strings.Contains(out.String(), "1 object(s) in error") {
		t.Fatalf("admin ord errors: %q", out.String())
	}
	if !a.alertRaised("ord_sync_failed:contract") {
		t.Fatal("no ord_sync_failed alert")
	}
	// T-11: через час без erid — оповещение проверки зависших.
	a.clk.Advance(61 * time.Minute)
	if err := a.mods.platform.stuck.Run(ctx, job.Running{}); err != nil {
		t.Fatal(err)
	}
	if !a.alertRaised("stuck:post_version_erid_overdue") {
		t.Fatal("no T-11 alert")
	}
	// Исправление и «Повторить» → erid и P-02.
	var contractID string
	if err := a.db.Pool.QueryRow(ctx, `SELECT id::text FROM ord_contract WHERE sync_status = 'error'`).Scan(&contractID); err != nil {
		t.Fatal(err)
	}
	a.adminClient("senior.orderr@example.ru", "senior")
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"ord", "retry", "--object", "contract:" + contractID,
		"--reason", "дата договора исправлена", "--email", "senior.orderr@example.ru"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	a.toTestClock()
	erid := a.untilErid(o.CurrentPostVersion.ID, 15)
	if o = adv.order(t, o.ID); o.Status != "in_progress" || o.Placements[0].Status != "awaiting_blogger" {
		t.Fatalf("order after retry: %s %s", o.Status, o.Placements[0].Status)
	}

	// S-28 шаг 2: ЕРИР отклоняет креатив после erid — запланированное размещение снова в created.
	pid := o.Placements[0].ID
	blogger := blg
	if r := blogger.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	a.schedule(blogger, pid, map[string]any{"mode": "at", "publish_at": at(15, 0, 0).Format(time.RFC3339)})
	var creativeID string
	if err := a.db.Pool.QueryRow(ctx, `SELECT id::text FROM creative WHERE post_version_id = $1`, o.CurrentPostVersion.ID).Scan(&creativeID); err != nil {
		t.Fatal(err)
	}
	a.ord.RejectObject(creativeID, "ERIR async error", "креатив отклонён")
	for range 3 {
		a.clk.Advance(time.Minute)
		a.settle()
	}
	if st, reason := a.placementState(pid); st != "created" || reason != "erir_rejected" {
		t.Fatalf("placement after ERIR rejection: %s %s", st, reason)
	}
	if a.count(`SELECT count(*) FROM job WHERE dedupe_key = $1 AND status = 'cancelled'`, "publish:"+pid) != 1 {
		t.Fatal("publish job not cancelled")
	}
	if !a.alertRaised("ord_creative_rejected") {
		t.Fatal("no ord_creative_rejected alert")
	}

	// S-29: новая версия поста — новый креатив и новый erid.
	var nv orderView
	if r := adv.do(http.MethodPost, "/orders/"+o.ID+"/post-versions", nil); r.status != http.StatusOK && r.status != http.StatusCreated {
		t.Fatalf("new version: %d %s", r.status, r.body)
	} else {
		r.json(t, &nv)
	}
	if nv.CurrentPostVersion.ID == o.CurrentPostVersion.ID {
		t.Fatal("version did not change")
	}
	a.approveVersion(nv.CurrentPostVersion.ID)
	erid2 := a.untilErid(nv.CurrentPostVersion.ID, 10)
	if erid2 == erid {
		t.Fatalf("new version got the old erid %s", erid2)
	}
	if a.count(`SELECT count(*) FROM creative`) != 2 {
		t.Fatal("want one creative per version")
	}
}

// S-29 после отклонения модерации: у новой версии свой креатив, старая без креатива.
func TestMarkingAfterModerationRejection(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	adv := a.advertiserClient("adv.ordrej@example.ru")
	a.acceptOffers(adv)
	ch := a.activeChannel(a.bloggerClient("blogger.ordrej@example.ru"), "Канал", 1_000_000)
	o := a.orderOn(adv, ch, "Отказ")
	first := uuid.MustParse(o.CurrentPostVersion.ID)
	var moderator uuid.UUID
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT created_by FROM "order" WHERE id = $1`, o.ID).Scan(&moderator); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mods.post.RejectVersion(context.Background(), first, moderator, "Нет плашки"); err != nil {
		t.Fatal(err)
	}
	var nv orderView
	adv.do(http.MethodPost, "/orders/"+o.ID+"/post-versions", nil).json(t, &nv)
	a.approveVersion(nv.CurrentPostVersion.ID)
	a.untilErid(nv.CurrentPostVersion.ID, 15)
	if a.count(`SELECT count(*) FROM creative WHERE post_version_id = $1`, first) != 0 {
		t.Fatal("rejected version got a creative")
	}
}

// checkMarkingLog — обязательные события маркировки (07 §5.3) и отсутствие персональных данных
// и секретов (07 §5.4): ИНН только маскированным, без наименований и текста поста.
func (a *testApp) checkMarkingLog(t *testing.T, secrets []string) {
	t.Helper()
	logs := a.logs.Bytes()
	messages := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(logs))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if m, ok := rec["msg"].(string); ok {
			messages[m] = true
		}
	}
	for _, m := range []string{
		"ord object queued", "ord object sent", "ord status polled", "ord object confirmed", "ord dependency not ready",
		"creative registration started", "erid received", "channel visible after ord platform", "platform organization ensured",
		"job started", "job finished", "job postponed",
	} {
		if !messages[m] {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(string(logs), s) {
			t.Errorf("log contains personal data or post text: %q", s)
		}
	}
}
