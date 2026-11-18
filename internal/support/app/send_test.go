package app

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/support/domain"
	"io"
	"testing"
	"time"
)

type immediateTx struct{}

func (immediateTx) InTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type sendRepo struct {
	Repository
	m        domain.Message
	answered int64
}

func (r *sendRepo) Message(context.Context, uuid.UUID, bool) (domain.Message, error) { return r.m, nil }
func (r *sendRepo) Claim(context.Context, uuid.UUID, time.Time) (domain.Message, bool, error) {
	r.m.Status = domain.Sending
	return r.m, true, nil
}
func (r *sendRepo) Conversation(context.Context, uuid.UUID, uuid.UUID, bool) (domain.Conversation, error) {
	return domain.Conversation{UserID: "9"}, nil
}
func (r *sendRepo) Complete(_ context.Context, m domain.Message, status, mid, reason string, _ time.Time) error {
	r.m.Status = status
	r.m.ExternalID = mid
	if status == domain.Sent {
		r.answered = m.ReplyToSequence
	}
	return nil
}

type sendProvider struct {
	err   error
	calls int
}

func (p *sendProvider) Send(context.Context, string, string, []domain.Attachment) (string, error) {
	p.calls++
	if p.err != nil {
		return "", p.err
	}
	return "mid-confirmed", nil
}
func (*sendProvider) Upload(context.Context, string, string, io.Reader) (string, error) {
	return "", errors.New("unexpected upload")
}
func (*sendProvider) ResolveVideo(context.Context, string) (string, error) {
	return "", errors.New("unexpected video")
}
func TestUnknownOutcomeNeverReplayed(t *testing.T) {
	r := &sendRepo{m: domain.Message{ID: ids.New(), ConversationID: ids.New(), Status: domain.Queued, ReplyToSequence: 4}}
	p := &sendProvider{err: &domain.DeliveryError{Reason: "timeout"}}
	s := &Service{Repo: r, Provider: p, Tx: immediateTx{}, Clock: clock.System{}, Log: logx.Discard()}
	for i := 0; i < 2; i++ {
		if e := s.Send(context.Background(), r.m.ID, 0); e != nil {
			t.Fatal(e)
		}
	}
	if p.calls != 1 || r.m.Status != domain.Unknown || r.answered != 0 {
		t.Fatal("unknown outcome replayed or marked answered")
	}
}
func TestKnownTemporaryRefusalRemainsQueued(t *testing.T) {
	r := &sendRepo{m: domain.Message{ID: ids.New(), ConversationID: ids.New(), Status: domain.Queued}}
	p := &sendProvider{err: &domain.DeliveryError{Reason: "rate_limited", Definitive: true, Temporary: true}}
	s := &Service{Repo: r, Provider: p, Tx: immediateTx{}, Clock: clock.System{}, Log: logx.Discard()}
	if e := s.Send(context.Background(), r.m.ID, 0); e == nil || r.m.Status != domain.Queued {
		t.Fatal("known refusal not retryable")
	}
	p.err = nil
	if e := s.Send(context.Background(), r.m.ID, 1); e != nil || r.m.Status != domain.Sent {
		t.Fatal("retry failed")
	}
}
