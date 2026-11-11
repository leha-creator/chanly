package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

func TestBudgetConcurrentReserveAndNoRetry(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	s := New(d.Owner, logx.Discard())
	_, err := d.Owner.Exec(ctx, `INSERT INTO rw_check_account(id,company_inn,total_limit,daily_limit,daily_operations,killed) VALUES('physical','1234567890',10000,2200,100,false)`)
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.Policy{AccountID: "physical", CompanyINN: "1234567890", Version: 1, FixedAmount: 1000, MaxCommission: 100, TotalBudget: 10000, DailyBudget: 2200, DailyOperations: 100, Executors: []domain.Executor{{ID: 1, INN: "123456789012", Employment: "selfemployed"}}, Actions: []domain.Action{domain.Create, domain.Pay}, NotBefore: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
	var runs []string
	for range 6 {
		grant := uuid.NewString()
		id := uuid.NewString()
		b, _ := json.Marshal(policy)
		policy.RunID = id
		policy.ManifestHash = strings.Repeat("a", 64)
		b, _ = json.Marshal(policy)
		_, err = d.Owner.Exec(ctx, `INSERT INTO rw_check_grant(id,account_id,version,purpose,issued_by,policy,not_before,expires_at) VALUES($1,'physical',1,'diagnostic','owner',$2,now()-interval '1 hour',now()+interval '1 hour')`, grant, b)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Prepare(ctx, domain.Run{ID: id, GrantID: grant, ManifestHash: strings.Repeat("a", 64), Manifest: []byte(`{}`), ExternalID: "rw-diagnostic:" + id, Nominal: 1000, FeeCeiling: 100})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, id)
	}
	var successes atomic.Int64
	var wg sync.WaitGroup
	for _, id := range runs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ok, err := s.Intent(ctx, id, domain.Create, strings.Repeat("b", 64))
			if err == nil && ok {
				successes.Add(1)
			}
		}(id)
	}
	wg.Wait()
	if successes.Load() != 2 {
		t.Fatalf("reserved=%d, want 2", successes.Load())
	}
	var reserved []string
	for _, id := range runs {
		r, err := s.Run(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.ReservedAt != nil {
			reserved = append(reserved, id)
		}
	}
	for range 100 {
		ok, err := s.Intent(ctx, reserved[0], domain.Create, strings.Repeat("b", 64))
		if err != nil || ok {
			t.Fatalf("retry permission=%v err=%v", ok, err)
		}
	}
	if err = s.Observe(ctx, reserved[0], domain.Create, "uncertain", nil); err != nil {
		t.Fatal(err)
	}
	// A new grant and a new day cannot release yesterday's unknown reservation.
	_, err = d.Owner.Exec(ctx, `ALTER TABLE rw_check_run DISABLE TRIGGER rw_check_run_guard; UPDATE rw_check_run SET reserved_at=now()-interval '1 day' WHERE reserved_at IS NOT NULL; ALTER TABLE rw_check_run ENABLE TRIGGER rw_check_run_guard`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range runs {
		r, _ := s.Run(ctx, id)
		if r.ReservedAt == nil {
			if ok, err := s.Intent(ctx, id, domain.Create, strings.Repeat("b", 64)); err == nil || ok {
				t.Fatal("midnight released hold")
			}
			break
		}
	}
	_, err = d.Owner.Exec(ctx, `UPDATE rw_check_account SET killed=true`)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Intent(ctx, reserved[1], domain.Pay, strings.Repeat("c", 64)); err == nil || ok {
		t.Fatal("kill switch ignored")
	}
	// Reconcile remains possible after kill: it cannot authorize another write.
	taskID := int64(123)
	if err = s.Observe(ctx, reserved[0], domain.Create, "observed", &taskID); err != nil {
		t.Fatal(err)
	}
}
