package server_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"maxim/mockapi/internal/seed"
)

// opCase — как вызвать операцию на начальных данных, чтобы получить успешный ответ.
type opCase struct {
	user   string            // email; пусто — без входа
	params map[string]string // параметры пути
	query  string
	// body: nil — пример из спецификации; noBody — без тела.
	body    any
	noBody  bool
	prepare func(e *env) (params map[string]string, body any)
	// status — ожидаемый статус вместо 2xx: операции, которых нет в тестовом сервере.
	status int
	// header — заголовки запроса (Idempotency-Key).
	header map[string]string
}

const (
	adv  = seed.EmailAdvertiser
	blg  = seed.EmailBlogger
	adm  = seed.EmailAdmin
	mod  = seed.EmailModerator
	adm2 = seed.EmailAdmin2
)

// approvableQueue сверяет номер РКН канала из очереди, чтобы его можно было одобрить.
func approvableQueue(e *env) (map[string]string, any) {
	path := "/admin/channels/" + seed.ChannelQueue.String()
	if r := e.do(e.client(adm), http.MethodPost, path+"/rkn-check", map[string]any{"result": "confirmed"}, ""); r.status != http.StatusOK {
		e.t.Fatalf("rkn-check: %d %s", r.status, r.body)
	}
	return id(seed.ChannelQueue), noBodyMarker
}

var noBodyMarker = struct{}{}

func id(u fmt.Stringer) map[string]string { return map[string]string{"id": u.String()} }

func at(d time.Duration) string {
	return time.Now().UTC().Add(d).Truncate(time.Second).Format(time.RFC3339)
}

func pngBytes() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	return b.Bytes()
}

func multipartFile(name string, data []byte) ([]byte, string) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, _ := w.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

// checkoutDraft оформляет заказ черновой кампании и возвращает его идентификатор.
func checkoutDraft(e *env) string {
	var o struct {
		ID string `json:"id"`
	}
	r := e.do(e.client(adv), http.MethodPost, "/campaigns/"+seed.CampaignDraft.String()+"/checkout", nil, "")
	if r.status != http.StatusCreated {
		e.t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	r.json(e.t, &o)
	return o.ID
}

var cases = map[string]opCase{
	// auth и общие
	"requestLoginCode":          {},
	"login":                     {},
	"logout":                    {user: adv, noBody: true},
	"getMe":                     {user: adv},
	"createOrganization":        {user: blg}, // блогер открывает кабинет рекламодателя
	"getCurrentOrganization":    {user: adv},
	"updateCurrentOrganization": {user: adv},
	"uploadTaxDocument":         {user: blg},
	"listLegalDocuments":        {user: adv},
	"acceptLegalDocuments":      {user: adv},
	"listNotifications":         {user: adv, query: "unread=true"},
	"markNotificationRead": {user: adv, prepare: func(e *env) (map[string]string, any) {
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		e.do(e.client(adv), http.MethodGet, "/notifications", nil, "").json(e.t, &page)
		return map[string]string{"id": page.Items[0].ID}, noBodyMarker
	}},
	"markAllNotificationsRead": {user: adv, noBody: true},

	// каталог и корзина
	"listCatalogChannels":   {user: adv, query: "format_id=f_1_24&price_to=1000000&sort=price_desc&limit=3"},
	"getCatalogChannel":     {user: adv, params: id(seed.ChannelFree)},
	"listChannelCategories": {user: adv},
	"listPlacementFormats":  {user: adv},
	"getCart":               {user: adv},
	"addCartItem":           {user: adv},
	"updateCartItem":        {user: adv, params: id(seed.CartItemFirst)},
	"deleteCartItem":        {user: adv, params: id(seed.CartItemFirst)},

	// кампании, пост, медиатека
	"listCampaigns": {user: adv, query: "status=ordered"},
	"createCampaign": {user: adv, body: map[string]any{
		"name": "Тестовая кампания", "date_from": time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
		"date_to": time.Now().AddDate(0, 0, 4).Format("2006-01-02"), "time_from": "09:00", "time_to": "21:00"}},
	"getCampaign":         {user: adv, params: id(seed.CampaignDraft)},
	"updateCampaign":      {user: adv, params: id(seed.CampaignDraft)},
	"getCampaignPost":     {user: adv, params: id(seed.CampaignDraft)},
	"saveCampaignPost":    {user: adv, params: id(seed.CampaignDraft)},
	"previewCampaignPost": {user: adv, params: id(seed.CampaignDraft), noBody: true},
	"checkoutCampaign":    {user: adv, params: id(seed.CampaignDraft), noBody: true},
	"listMedia":           {user: adv, query: "limit=2"},
	"uploadMedia":         {user: adv},
	"deleteMedia":         {user: adv, params: id(seed.MediaFirst)},

	// заказы и размещения рекламодателя
	"listOrders":             {user: adv},
	"getOrder":               {user: adv, params: id(seed.OrderActive)},
	"createOrderPostVersion": {user: adv, params: id(seed.OrderActive), noBody: true},
	"getOrderReport":         {user: adv, params: id(seed.OrderPast)},
	"getOrderReportCsv":      {user: adv, params: id(seed.OrderPast)},
	"cancelPlacement":        {user: adv, params: id(seed.PlacementAwaitingBlogger), noBody: true},
	"cancelOrder":            {user: adv, params: id(seed.OrderActive), noBody: true},
	"acceptPlacement":        {user: adv, params: id(seed.PlacementRemoved), noBody: true},
	"createPlacementClaim":   {user: adv, params: id(seed.PlacementPublished)},

	// баланс
	"getBalance":              {user: adv},
	"listBalanceTransactions": {user: adv},
	"listInvoices":            {user: adv},
	"createInvoice":           {user: adv},
	"getInvoicePdf":           {user: adv, params: id(seed.InvoicePaid)},
	"listClosingActs":         {user: adv},
	"getClosingActPdf":        {user: adv, params: map[string]string{"id": seed.ClosingActPast.String(), "document": "report"}},

	// блогер
	"listBloggerChannels":     {user: blg},
	"createChannelLinkCode":   {user: blg, noBody: true},
	"createChannelConnection": {user: blg, noBody: true},
	"getChannelConnection": {user: blg, prepare: func(e *env) (map[string]string, any) {
		id, _ := prepareConnection(e)
		return map[string]string{"id": id}, noBodyMarker
	}},
	"refreshChannelConnection": {user: blg, prepare: func(e *env) (map[string]string, any) {
		id, _ := prepareConnection(e)
		e.do(e.client(""), http.MethodPost, "/_mock/clock", map[string]any{"hours": 0.1}, "")
		return map[string]string{"id": id}, noBodyMarker
	}},
	"claimChannelConnection": {user: blg, prepare: func(e *env) (map[string]string, any) {
		id, ch := prepareConnection(e)
		return map[string]string{"id": id}, map[string]any{"channel_id": ch}
	}},
	"getBloggerChannel":       {user: blg, params: id(seed.ChannelBlogger1)},
	"updateBloggerChannel":    {user: blg, params: id(seed.ChannelModerate)},
	"setBloggerChannelPrices": {user: blg, params: id(seed.ChannelBlogger1)},
	"addChannelUnavailability": {user: blg, params: id(seed.ChannelBlogger1),
		body: map[string]any{"starts_at": at(240 * time.Hour), "ends_at": at(264 * time.Hour)}},
	"deleteChannelUnavailability": {user: blg, params: map[string]string{"id": seed.ID(103).String(), "unavailabilityId": seed.ID(1001).String()}},
	"listBloggerPlacements":       {user: blg, query: "status=awaiting_blogger"},
	"getBloggerPlacement":         {user: blg, params: id(seed.PlacementAwaitingBlogger)},
	"bloggerAcceptPlacement":      {user: blg, params: id(seed.PlacementAwaitingBlogger), noBody: true},
	"bloggerDeclinePlacement":     {user: blg, params: id(seed.PlacementAwaitingBlogger)},
	"bloggerSchedulePlacement": {user: blg, params: id(seed.PlacementAwaitingSchedule),
		body: map[string]any{"mode": "at", "publish_at": at(2 * time.Hour)}},
	"bloggerReschedulePlacement": {user: blg, params: id(seed.PlacementScheduled),
		body: map[string]any{"publish_at": at(5 * time.Hour)}},
	"listBloggerPayouts":      {user: blg},
	"getBloggerPayoutSummary": {user: blg},
	"getRocketworkStatus":     {user: blg},
	"inviteRocketwork":        {user: blg, noBody: true},
	"requestRocketworkAction": {user: blg, body: map[string]any{"action": "invite"}},
	"refreshRocketworkStatus": {user: blg, noBody: true},

	// администратор
	"listAdminChannels":  {user: adm, query: "status=moderation"},
	"getAdminChannel":    {user: adm, params: id(seed.ChannelQueue)},
	"approveChannel":     {user: adm, prepare: approvableQueue},
	"rejectChannel":      {user: adm, params: id(seed.ChannelModerate)},
	"setChannelRknCheck": {user: adm, params: id(seed.ChannelQueue)},
	"blockChannel":       {user: adm, params: id(seed.ChannelFree)},
	"unblockChannel": {user: adm, prepare: func(e *env) (map[string]string, any) {
		e.post(adm, "/admin/channels/"+seed.ChannelFree.String()+"/block", map[string]any{"reason": "Проверка"}, nil)
		return id(seed.ChannelFree), nil
	}},
	"createChannelCategory": {user: adm},
	"updateChannelCategory": {user: adm, params: id(seed.CategoryFood)},
	"listAdminOrders":       {user: adm, query: "status=all"},
	"getAdminOrder":         {user: adm, params: id(seed.OrderActive)},
	"approveOrderPost": {user: adm, prepare: func(e *env) (map[string]string, any) {
		return map[string]string{"id": checkoutDraft(e)}, noBodyMarker
	}},
	"rejectOrderPost": {user: adm, prepare: func(e *env) (map[string]string, any) {
		return map[string]string{"id": checkoutDraft(e)}, nil
	}},
	"listAdminPlacements":  {user: adm, query: "status=disputed"},
	"getAdminPlacement":    {user: adm, params: id(seed.PlacementDisputed)},
	"adminCancelPlacement": {user: adm, params: id(seed.PlacementScheduled)},
	"adminRemovePost":      {user: adm, params: id(seed.PlacementPublished)},
	"adminMarkRemoved": {user: adm, params: id(seed.PlacementPublished),
		body: map[string]any{"removed_at": at(-time.Hour), "reason": "Блогер удалил пост вручную"}},
	"adminOpenDispute":              {user: adm, params: id(seed.PlacementRemoved)},
	"listAdminDisputes":             {user: adm},
	"getAdminDispute":               {user: adm, params: id(seed.DisputeOpen)},
	"previewDisputeResolution":      {user: adm, params: id(seed.DisputeOpen), query: "resolution=proportional&share=7500"},
	"resolveDispute":                {user: adm, params: id(seed.DisputeOpen)},
	"listAdminDisputeMessages":      {user: adm, params: id(seed.DisputeOpen)},
	"createAdminDisputeMessage":     {user: adm, params: id(seed.DisputeOpen)},
	"listPlacementDisputeMessages":  {user: adv, params: id(seed.PlacementDisputed)},
	"createPlacementDisputeMessage": {user: adv, params: id(seed.PlacementDisputed)},
	"listBloggerDisputeMessages":    {user: blg, prepare: bloggerDispute},
	"createBloggerDisputeMessage": {user: blg, prepare: func(e *env) (map[string]string, any) {
		p, _ := bloggerDispute(e)
		return p, nil
	}},
	"listAdminPayouts":          {user: adm, query: "status=failed"},
	"getAdminRocketworkCheck":   {user: adm},
	"startAdminRocketworkCheck": {user: adm, noBody: true, status: http.StatusServiceUnavailable, header: map[string]string{"If-Match": "00000000-0000-4000-8000-000000000001"}},
	"getAdminPayoutSummary":     {user: adm},
	"exportAdminPayouts":        {user: adm},
	"retryPayout":               {user: adm, params: id(seed.PayoutFailed)},
	"markPayoutPaid":            {user: adm, params: id(seed.PayoutBankTransfer), header: idem("mark-paid-1")},
	"uploadBankStatement":       {user: adm},
	"getLatestBankStatement":    {user: adm},
	"listBankTransactions":      {user: adm, query: "match_status=unmatched"},
	"matchBankTransaction":      {user: adm, params: id(seed.BankTxUnmatched)},
	"ignoreBankTransaction":     {user: adm, params: id(seed.BankTxUnmatched)},
	"listAdminInvoices":         {user: adm},
	"cancelAdminInvoice":        {user: adm, params: id(seed.InvoiceIssued)},
	"getLedgerSummary":          {user: adm},
	"listLedgerEntries":         {user: adm},
	"createManualLedgerEntry":   {user: adm, header: idem("manual-1")},
	"createDepositTopup":        {user: adm, header: idem("topup-1")},
	"createBalanceReturn":       {user: adm, params: id(seed.OrgAdvertiser), header: idem("return-1")},
	"listAdminApprovals":        {user: adm},
	"approveAdminApproval":      {user: adm2, prepare: pendingApproval},
	"rejectAdminApproval": {user: adm, prepare: func(e *env) (map[string]string, any) {
		p, _ := pendingApproval(e)
		return p, nil
	}},
	"listAdminOrganizations":  {user: adm, query: "query=7700"},
	"getAdminOrganization":    {user: adm, params: id(seed.OrgAdvertiser)},
	"updateAdminOrganization": {user: adm, params: id(seed.OrgAdvertiser)},
	"blockUser":               {user: adm, params: id(seed.UserBlogger)},
	"unblockUser": {user: adm, prepare: func(e *env) (map[string]string, any) {
		e.post(adm, "/admin/users/"+seed.UserBlogger.String()+"/block", map[string]any{"reason": "Проверка"}, nil)
		return id(seed.UserBlogger), nil
	}},
	"listAuditLog":     {user: adm, query: "manual_only=true"},
	"listOrdErrors":    {user: adm},
	"getOrdObject":     {user: adm, params: map[string]string{"type": "contract", "id": seed.OrdContractError.String()}},
	"retryOrdObject":   {user: adm, params: map[string]string{"type": "contract", "id": seed.OrdContractError.String()}},
	"listOrdReports":   {user: adm},
	"getOrdReport":     {user: adm, params: prevPeriod()},
	"rebuildOrdReport": {user: adm, params: prevPeriod()},
	"sendOrdReport":    {user: adm, params: prevPeriod()},
	"getAdminCounters": {user: adm},

	// webhooks
	"maxWebhook":        {params: map[string]string{"secret": "test-secret"}},
	"rocketworkWebhook": {},

	// _mock
	"mockReset":      {noBody: true},
	"mockGetClock":   {},
	"mockShiftClock": {},
	"mockApproveOrderPost": {prepare: func(e *env) (map[string]string, any) {
		return map[string]string{"id": checkoutDraft(e)}, noBodyMarker
	}},
	"mockRejectOrderPost": {prepare: func(e *env) (map[string]string, any) {
		return map[string]string{"id": checkoutDraft(e)}, nil
	}},
	"mockAdvancePlacement": {params: id(seed.PlacementScheduled)},
	"mockPayInvoice":       {params: id(seed.InvoiceIssued), noBody: true},
	"mockLinkChannel": {prepare: func(e *env) (map[string]string, any) {
		var lc struct {
			Code string `json:"code"`
		}
		e.do(e.client(blg), http.MethodPost, "/blogger/channels/link-code", nil, "").json(e.t, &lc)
		return nil, map[string]any{"code": lc.Code, "title": "Канал из теста", "subscribers_count": 500}
	}},
}

// TestEveryOperation вызывает каждую операцию спецификации на свежих начальных
// данных и проверяет, что ответ успешный и совпадает со схемой.
func TestEveryOperation(t *testing.T) {
	e := newEnv(t)
	type op struct{ method, path, id string }
	var ops []op
	for path, item := range e.spec.Paths.Map() {
		for method, o := range item.Operations() {
			// oapi-codegen встраивает спецификацию с operationId в PascalCase.
			ops = append(ops, op{method, path, strings.ToLower(o.OperationID[:1]) + o.OperationID[1:]})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].id < ops[j].id })

	for _, o := range ops {
		t.Run(o.id, func(t *testing.T) {
			c, ok := cases[o.id]
			if !ok {
				t.Fatalf("operation %s has no test case", o.id)
			}
			e.t = t
			e.st.Reset(context.Background())

			params, body := c.params, c.body
			if c.prepare != nil {
				p, b := c.prepare(e)
				if p != nil {
					params = p
				}
				if b != nil {
					body = b
				}
			}
			contentType := ""
			if body == noBodyMarker || c.noBody {
				body = nil
			} else if body == nil {
				body, contentType = exampleBody(e, o.path, o.method)
			}
			path := o.path
			for k, v := range params {
				path = strings.ReplaceAll(path, "{"+k+"}", v)
			}
			if strings.Contains(path, "{") {
				t.Fatalf("path parameters are not set: %s", path)
			}
			if c.query != "" {
				path += "?" + c.query
			}
			r := e.doH(e.client(c.user), o.method, path, body, contentType, c.header)
			if c.status != 0 {
				if r.status != c.status {
					t.Errorf("%s %s: status %d, want %d\nbody=%s", o.method, path, r.status, c.status, truncate(r.body))
				}
				return
			}
			if r.status < 200 || r.status > 299 {
				t.Errorf("%s %s: status %d, want 2xx\nrequest_id=%s body=%s", o.method, path, r.status, r.header.Get("X-Request-Id"), truncate(r.body))
			}
		})
	}
	inSpec := map[string]bool{}
	for _, o := range ops {
		inSpec[o.id] = true
	}
	for name := range cases {
		if !inSpec[name] {
			t.Errorf("test case %s has no operation in spec", name)
		}
	}
}

// exampleBody берёт тело запроса из примера спецификации.
func exampleBody(e *env, path, method string) (any, string) {
	o := e.spec.Paths.Find(path).GetOperation(method)
	if o.RequestBody == nil {
		return nil, ""
	}
	content := o.RequestBody.Value.Content
	if mt := content.Get("multipart/form-data"); mt != nil {
		b, ct := multipartFile("test.png", pngBytes())
		return b, ct
	}
	mt := content.Get("application/json")
	if mt == nil || mt.Example == nil {
		if o.RequestBody.Value.Required {
			e.t.Fatalf("%s %s: required body has no example and no test body", method, path)
		}
		return nil, ""
	}
	return mt.Example, "application/json"
}
