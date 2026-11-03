package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/integration/ord/ordfake"
	"github.com/leha-creator/chanly/internal/marking/domain"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

type recordingListener struct {
	name  string
	calls *[]string
	err   error
}

func (l recordingListener) VersionChanged(context.Context, postdomain.VersionChange) error {
	*l.calls = append(*l.calls, l.name)
	return l.err
}

func TestVersionListenersOrder(t *testing.T) {
	var calls []string
	ls := VersionListeners{recordingListener{"placement", &calls, nil}, recordingListener{"marking", &calls, nil}}
	if err := ls.VersionChanged(context.Background(), postdomain.VersionChange{}); err != nil || len(calls) != 2 ||
		calls[0] != "placement" || calls[1] != "marking" {
		t.Fatalf("calls %v err %v", calls, err)
	}
	calls = nil
	boom := errors.New("boom")
	ls = VersionListeners{recordingListener{"placement", &calls, boom}, recordingListener{"marking", &calls, nil}}
	if err := ls.VersionChanged(context.Background(), postdomain.VersionChange{}); !errors.Is(err, boom) || len(calls) != 1 {
		t.Fatalf("first error must stop: calls %v err %v", calls, err)
	}
}

// Тело, которое уходит в ОРД, совпадает с запросом домена: хеш нагрузки считается по тому же JSON.
func TestMarkingOrdSendsDomainPayload(t *testing.T) {
	fake := ordfake.New()
	b := MarkingOrd{Client: fake}
	ctx := context.Background()
	c := domain.NewCreative(uuid.New(), uuid.New(), time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC))
	req := domain.NewCreativeRequest(c, "contract-1", domain.CreativeContent{
		Text: "Текст", URLs: []string{"https://a.test/"}, ImageURLs: []string{"https://cdn/1.png"},
	}, "30.15.1")
	rec, err := b.UpsertCreative(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, tok, _ := rec.For(c.ExternalID()); tok != "fake-erid-1" {
		t.Fatalf("token %q", tok)
	}
	obj, _ := fake.Object(c.ExternalID())
	want, _ := json.Marshal(req)
	if !sameJSON(obj.Body, want) {
		t.Fatalf("sent %s, domain %s", obj.Body, want)
	}

	agent := false
	ct := domain.ContractRequest{ID: "c1", Type: domain.OrdIntermediaryContract, ContractorID: "p", ClientID: "a",
		IsRegReport: true, Date: "2026-10-17", AgentActingForPublisher: &agent}
	if _, err := b.UpsertContract(ctx, ct); err != nil {
		t.Fatal(err)
	}
	obj, _ = fake.Object("c1")
	want, _ = json.Marshal(ct)
	if !sameJSON(obj.Body, want) {
		t.Fatalf("contract sent %s, domain %s", obj.Body, want)
	}
	var sent ord.Contract
	if err := obj.Decode(&sent); err != nil || sent.AgentActingForPublisher == nil {
		t.Fatalf("decode: %+v %v", sent, err)
	}
}

func sameJSON(a, b []byte) bool {
	ha, err1 := domain.PayloadHash(json.RawMessage(a))
	hb, err2 := domain.PayloadHash(json.RawMessage(b))
	return err1 == nil && err2 == nil && ha == hb
}
