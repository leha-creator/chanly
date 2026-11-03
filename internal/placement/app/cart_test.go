package app_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/placementtest"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestCart(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Рецепты", 1_000_000)

	cart, err := e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, ch, "f_1_24")
	if err != nil {
		t.Fatal(err)
	}
	// 06 §3: 10 000,00 ₽ и комиссия 15 % — 11 500,00 ₽.
	if len(cart.Lines) != 1 || cart.Total != 1_150_000 || cart.Lines[0].Item.ShownTotal != 1_150_000 ||
		!cart.Lines[0].Available || cart.Lines[0].PriceChanged || cart.Lines[0].Channel.Title != "Рецепты" {
		t.Fatalf("cart %+v", cart)
	}
	_, err = e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, ch, "f_1_24")
	wantReason(t, err, apperr.CodeConflict, "already_in_cart")
	_, err = e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, ch, "f_2_48")
	wantReason(t, err, apperr.CodeConflict, "already_in_cart")
	if _, err := e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, uuid.New(), "f_1_24"); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}

	// Свой канал: рекламодатель — владелец канала.
	own := placementtest.Channel(t, e.d.Pool, e.adv, "Свой", 1_000_000)
	_, err = e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, own, "f_1_24")
	wantReason(t, err, apperr.CodeValidationFailed, "own_channel")
	_, _, other := e.blogger("Другой", 500_000)
	_, err = e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, other, "f_2_48")
	wantReason(t, err, apperr.CodeValidationFailed, "format_unavailable")

	// Блогер поднял цену: price_changed; тот же формат подтверждает цену.
	placementtest.SetPrice(t, e.d.Pool, ch, 1_200_000)
	cart, err = e.svc.GetCart(context.Background(), e.adv)
	if err != nil || !cart.Lines[0].PriceChanged || *cart.Lines[0].Current != 1_380_000 || cart.Total != 1_380_000 {
		t.Fatalf("price changed: %+v %v", cart.Lines, err)
	}
	item := cart.Lines[0].Item.ID
	cart, err = e.svc.UpdateCartItem(e.ctx(), e.adv, item, "f_1_24")
	if err != nil || cart.Lines[0].PriceChanged || cart.Lines[0].Item.ShownTotal != 1_380_000 {
		t.Fatalf("price confirmed: %+v %v", cart.Lines, err)
	}
	_, err = e.svc.UpdateCartItem(e.ctx(), e.adv, item, "f_3_72")
	wantReason(t, err, apperr.CodeValidationFailed, "format_unavailable")

	// Канал приостановлен: позиция остаётся, но недоступна.
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'suspended' WHERE id = $1`, ch); err != nil {
		t.Fatal(err)
	}
	cart, err = e.svc.GetCart(context.Background(), e.adv)
	if err != nil || len(cart.Lines) != 1 || cart.Lines[0].Available || cart.Total != 0 {
		t.Fatalf("suspended channel: %+v %v", cart, err)
	}
	// Канал вне каталога не добавляется.
	_, _, hidden := e.blogger("Скрытый", 1_000_000)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'suspended' WHERE id = $1`, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, hidden, "f_1_24"); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("hidden channel: %v", err)
	}

	// Чужая позиция — 404.
	stranger, strangerUser := placementtest.Blogger(t, e.d.Pool)
	if _, err := e.svc.DeleteCartItem(e.ctx(), stranger, item); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if _, err := e.svc.UpdateCartItem(e.ctx(), stranger, item, "f_1_24"); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	_ = strangerUser
	if cart, err := e.svc.DeleteCartItem(e.ctx(), e.adv, item); err != nil || len(cart.Lines) != 0 {
		t.Fatalf("delete: %+v %v", cart, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action LIKE 'cart.%'`); n != 3 {
		t.Fatalf("audit entries %d", n)
	}
	if len(e.logged("cart item rejected")) < 5 || len(e.logged("cart item added")) != 1 {
		t.Fatal("cart log events")
	}
}

func TestCartLimit(t *testing.T) {
	e := newEnv(t)
	org, _, _ := e.blogger("Первый", 1_000_000)
	// 50 позиций — записью напрямую; 51-я через сценарий — cart_full.
	for i := range 50 {
		ch := placementtest.Channel(t, e.d.Pool, org, "Канал", 1_000_000)
		if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO cart_item (id, organization_id, channel_id, format_id,
			shown_total_amount, created_by, created_at, updated_at) VALUES ($1, $2, $3, 'f_1_24', 1150000, $4, now(), now())`,
			ids.New(), e.adv, ch, e.advUser); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	last := placementtest.Channel(t, e.d.Pool, org, "Канал 51", 1_000_000)
	_, err := e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, last, "f_1_24")
	wantReason(t, err, apperr.CodeValidationFailed, "cart_full")
	in, err := e.svc.InCart(context.Background(), e.adv, []uuid.UUID{last})
	if err != nil || in[last] {
		t.Fatalf("in cart %v %v", in, err)
	}
}
