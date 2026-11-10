package rocketwork_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworktest"
)

type taskScenarioAPI interface {
	CreateTask(context.Context, rocketwork.TaskTerms) (rocketwork.TaskSnapshot, error)
	GetTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
	FindTask(context.Context, string) (rocketwork.TaskSnapshot, bool, error)
	CompleteTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
	SignTaskAct(context.Context, int64) (rocketwork.TaskSnapshot, error)
	PayTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
}

func TestTaskScenarioAgainstFakeAndContractHTTP(t *testing.T) {
	for _, variant := range []string{"fake", "http"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			terms := syntheticTerms("manual")
			terms.AmountKopecks = 10001
			var api taskScenarioAPI
			var lose func()
			var paid func(int64)
			var hide func()
			var reveal func()
			if variant == "fake" {
				f := rocketworkfake.New([]byte("synthetic"), rocketworkfake.WithStatePath(filepath.Join(t.TempDir(), "fake.json")))
				api = f
				lose = func() { f.LoseTaskResponse("create") }
				hide = func() { f.HideTaskLookups(1) }
				reveal = func() {}
				paid = func(id int64) {
					fee := int64(100)
					if err := f.ObserveTask(id, "paid", &fee, "https://receipt.example.test/1"); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				e := rocketworktest.New(t)
				e.Token = token
				e.ConfigureExecutor(terms.ExecutorINN, "+79000000000", "entrepreneur", "all_is_well")
				c, _ := newClient(t, e.ServeHTTP)
				api = c
				lose = func() { e.LoseNext("POST", "/api/tasks") }
				hide = func() { e.HideLookups(1) }
				reveal = func() {}
				paid = func(id int64) {
					e.SetTask(id, map[string]any{"status": "paid", "commission": "1.00", "receipt_uri": "https://receipt.example.test/1"})
				}
			}
			lose()
			if _, err := api.CreateTask(ctx, terms); !errors.Is(err, rocketwork.ErrUncertain) {
				t.Fatalf("lost response: %v", err)
			}
			hide()
			if _, found, err := api.FindTask(ctx, terms.ExternalID); err != nil || found {
				t.Fatalf("delayed visibility: %t %v", found, err)
			}
			reveal()
			created, found, err := api.FindTask(ctx, terms.ExternalID)
			if err != nil || !found {
				t.Fatalf("recovery: %t %v", found, err)
			}
			for _, step := range []struct {
				name, status string
				call         func(context.Context, int64) (rocketwork.TaskSnapshot, error)
			}{{"complete", "accepting", api.CompleteTask}, {"act", "completed", api.SignTaskAct}, {"pay", "paying", api.PayTask}} {
				if _, err := step.call(ctx, created.ID); err != nil {
					t.Fatal(step.name, err)
				}
				state, err := api.GetTask(ctx, created.ID)
				if err != nil || state.Status != step.status {
					t.Fatalf("%s: %+v %v", step.name, state, err)
				}
			}
			paid(created.ID)
			state, err := api.GetTask(ctx, created.ID)
			if err != nil || state.Status != "paid" || state.CommissionKopecks == nil || *state.CommissionKopecks != 100 || state.AmountKopecks != 10001 || state.ReceiptURL == "" {
				t.Fatalf("paid: %+v %v", state, err)
			}
			if _, err := api.PayTask(ctx, created.ID); !errors.Is(err, rocketwork.ErrFormat) {
				t.Fatal("paid task was paid again")
			}
		})
	}
}

func TestContractEmulatorPaginationAndLostPay(t *testing.T) {
	e := rocketworktest.New(t)
	e.Token = token
	e.ConfigureExecutor("000000000000", "+79000000000", "entrepreneur", "all_is_well")
	c, _ := newClient(t, e.ServeHTTP)
	ctx := context.Background()
	terms := syntheticTerms("manual")
	created, err := c.CreateTask(ctx, terms)
	if err != nil {
		t.Fatal(err)
	}
	e.DuplicateLookup(true)
	if _, _, err := c.FindTask(ctx, terms.ExternalID); !errors.Is(err, rocketwork.ErrFormat) {
		t.Fatal("duplicate on second page accepted")
	}
	e.DuplicateLookup(false)
	if _, err := c.CompleteTask(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SignTaskAct(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d/pay", created.ID)
	e.LoseNext("PATCH", path)
	if _, err := c.PayTask(ctx, created.ID); !errors.Is(err, rocketwork.ErrUncertain) {
		t.Fatal(err)
	}
	state, err := c.GetTask(ctx, created.ID)
	if err != nil || state.Status != "paying" {
		t.Fatal("lost pay not observable")
	}
	if e.WriteCount("PATCH", path) != 1 {
		t.Fatal("transport replayed pay")
	}
}
