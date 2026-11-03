package bootstrap

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
)

type balanceOperation struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
	OrderID     string `json:"order_id"`
	PlacementID string `json:"placement_id"`
	CreatedAt   string `json:"created_at"`
}

type operationsPage struct {
	Items      []balanceOperation `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

func TestBalanceHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("adv-balance-page@example.ru")
	a.fund("adv-balance-page@example.ru", 3_000_000)
	blg := a.bloggerClient("blg-balance-page@example.ru")
	ch1 := a.activeChannel(blg, "Первый", 1_000_000)
	ch2 := a.activeChannel(blg, "Второй", 200_000)
	campaign := a.campaignWithPost(adv, "Баланс")
	for _, ch := range []string{ch1, ch2} {
		if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
			t.Fatalf("add to cart: %d %s", r.status, r.body)
		}
	}
	var order orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil).json(t, &order)
	p1, p2 := a.placementOf(order.ID, ch1), a.placementOf(order.ID, ch2)

	var bal struct {
		Available int64 `json:"available_amount"`
		Held      int64 `json:"held_amount"`
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 3_000_000-1_150_000-230_000 || bal.Held != 1_380_000 {
		t.Fatalf("balance after checkout %+v", bal)
	}

	// Второе размещение отменено — удержание снято; первое распределено с долей 75 %.
	if r := adv.do(http.MethodPost, "/placements/"+p2+"/cancel", nil); r.status != http.StatusOK {
		t.Fatalf("cancel placement: %d %s", r.status, r.body)
	}
	if err := a.mods.txRunner.InTx(context.Background(), func(ctx context.Context) error {
		return a.mods.ledger.Settle(ctx, uuid.MustParse(p1), blg.orgID(t),
			ledgerdomain.Distribution{Payout: 750_000, FeeCharged: 112_500, Refund: 287_500}, ledgerdomain.ActorSystem, uuid.Nil)
	}); err != nil {
		t.Fatal(err)
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Held != 0 || bal.Available != 3_000_000-1_150_000+287_500 {
		t.Fatalf("balance after settlement %+v", bal)
	}

	// Операции новые сверху; распределение блогеру и платформе не показывается.
	var all []balanceOperation
	cursor := ""
	for {
		var page operationsPage
		path := "/balance/transactions?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		adv.do(http.MethodGet, path, nil).json(t, &page)
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	want := []struct {
		kind   string
		amount int64
		desc   string
	}{
		{"refund", 287_500, "Возврат по заказу № "},
		{"hold_release", 230_000, "Снятие удержания по заказу № "},
		{"hold", -230_000, "Удержание по заказу № "},
		{"hold", -1_150_000, "Удержание по заказу № "},
		{"deposit", 3_000_000, "Пополнение баланса"},
	}
	if len(all) != len(want) {
		t.Fatalf("operations %+v", all)
	}
	// Удержания одной транзакции базы имеют одно время: порядок между ними не задан.
	if all[2].Amount+all[3].Amount != -1_380_000 {
		t.Fatalf("holds %+v %+v", all[2], all[3])
	}
	all[2].Amount, all[3].Amount = -230_000, -1_150_000
	for i, w := range want {
		got := all[i]
		if got.Kind != w.kind || got.Amount != w.amount || len(got.Description) < len(w.desc) || got.Description[:len(w.desc)] != w.desc {
			t.Errorf("operation %d = %+v, want %+v", i, got, w)
		}
		if w.kind != "deposit" && (got.OrderID != order.ID || got.PlacementID == "") {
			t.Errorf("operation %d refs %+v", i, got)
		}
	}

	if r := blg.do(http.MethodGet, "/balance", nil); r.status != http.StatusForbidden {
		t.Fatalf("blogger balance: %d", r.status)
	}
	if r := adv.do(http.MethodGet, "/balance/transactions?cursor=bad", nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad cursor: %d", r.status)
	}
}
