package ordfake_test

import (
	"context"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/integration/ord/ordfake"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func TestStatusProgression(t *testing.T) {
	ctx := context.Background()
	f := ordfake.New()
	rec, err := f.UpsertOrganization(ctx, ord.Organization{ID: "org-1", Type: "ul"})
	if err != nil || rec.Status != "Request accepted by ORD" || rec.RequestID == "" {
		t.Fatalf("upsert: %+v %v", rec, err)
	}
	for _, want := range []string{"ORD success", "ERIR sync success", "ERIR async success", "ERIR async success"} {
		st, err := f.GetStatus(ctx, rec.RequestID)
		if err != nil || st.ObjectStatuses[0].Status != want {
			t.Fatalf("status %+v %v, want %s", st, err, want)
		}
	}
	// Площадка завершается на ORD success.
	rec, _ = f.UpsertPlatforms(ctx, ord.Platforms{OrganizationID: "org-1", Platforms: []ord.Platform{{PlatformID: "ch-1"}}})
	for range 3 {
		_, _ = f.GetStatus(ctx, rec.RequestID)
	}
	if o, _ := f.Object("ch-1"); o.Status != "ORD success" {
		t.Fatalf("platform status %s", o.Status)
	}
	// Повтор с тем же id — тот же объект.
	_, _ = f.UpsertOrganization(ctx, ord.Organization{ID: "org-1", Type: "ul", Name: "Новое"})
	if o, _ := f.Object("org-1"); o.Submits != 2 || len(f.Objects()) != 2 {
		t.Fatalf("resubmit: %+v, objects %d", o, len(f.Objects()))
	}
}

func TestCreativeTokenAndFailures(t *testing.T) {
	ctx := context.Background()
	f := ordfake.New()
	rec, err := f.UpsertCreative(ctx, ord.Creative{ID: "cr-1"})
	if err != nil || rec.Token != "fake-erid-1" || rec.ObjectStatuses[0].Token != "fake-erid-1" {
		t.Fatalf("creative: %+v %v", rec, err)
	}
	f.RejectObject("cr-1", "ERIR async error", "креатив отклонён")
	st, _ := f.GetStatus(ctx, rec.RequestID)
	if st.ObjectStatuses[0].Status != "ERIR async error" || st.ObjectStatuses[0].Errors()[0] != "креатив отклонён" {
		t.Fatalf("rejected: %+v", st)
	}

	f.TokenOnlyInStatus()
	rec, _ = f.UpsertCreative(ctx, ord.Creative{ID: "cr-2"})
	if rec.Token != "" {
		t.Fatalf("token in POST response: %+v", rec)
	}
	if st, _ := f.GetStatus(ctx, rec.RequestID); st.ObjectStatuses[0].Token != "fake-erid-2" {
		t.Fatalf("token not in status: %+v", st)
	}

	f.FailNext(ord.OpContract, 422, "body.date: field required")
	if _, err := f.UpsertContract(ctx, ord.Contract{ID: "ct-1"}); !job.IsPermanent(err) {
		t.Fatalf("422 must be permanent: %v", err)
	}
	f.FailNext(ord.OpContract, 503, "unavailable")
	if _, err := f.UpsertContract(ctx, ord.Contract{ID: "ct-1"}); err == nil || job.IsPermanent(err) {
		t.Fatalf("503 must be retryable: %v", err)
	}
	f.TimeoutNext(ord.OpContract)
	if _, err := f.UpsertContract(ctx, ord.Contract{ID: "ct-1"}); err == nil {
		t.Fatal("timeout must return an error")
	}
	if _, ok := f.Object("ct-1"); !ok || f.Calls(ord.OpContract) != 3 {
		t.Fatalf("timeout must store the object; calls %d", f.Calls(ord.OpContract))
	}
}

func TestReportingObjectsWaitForErir(t *testing.T) {
	ctx := context.Background()
	f := ordfake.New()
	st, _ := f.UpsertStatistics(ctx, ord.Statistics{ID: "stat-1", CreativeID: "cr-1", PlatformID: "ch-1"})
	_, _ = f.UpsertInvoice(ctx, ord.Invoice{ID: "inv-1", ContractID: "ct-1"})
	ic, _ := f.UpsertInvoiceCreatives(ctx, ord.InvoiceCreatives{InvoiceID: "inv-1"})
	for range 3 {
		_, _ = f.GetStatus(ctx, st.RequestID)
		_, _ = f.GetStatus(ctx, ic.RequestID)
	}
	for _, id := range []string{"stat-1", "inv-1", ordfake.InvoiceCreativesID("inv-1")} {
		if o, ok := f.Object(id); !ok || o.Status != "ERIR async success" && id != "inv-1" {
			t.Fatalf("%s: %+v", id, o)
		}
	}
	f.FailNext(ord.OpStatistics, 422, "body.amount: invalid")
	if _, err := f.UpsertStatistics(ctx, ord.Statistics{ID: "stat-2"}); !job.IsPermanent(err) {
		t.Fatalf("statistics failure: %v", err)
	}
	if f.Calls(ord.OpInvoiceCreatives) != 1 {
		t.Fatalf("invoice creatives calls %d", f.Calls(ord.OpInvoiceCreatives))
	}
}
