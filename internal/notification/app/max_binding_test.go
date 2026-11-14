package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type boundRecipients struct {
	domain.Recipients
	members []uuid.UUID
	max     map[uuid.UUID]int64
	err     error
}

func (r boundRecipients) Members(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return r.members, nil
}

func (r boundRecipients) NotificationMAXRecipient(_ context.Context, user uuid.UUID) (int64, error) {
	return r.max[user], r.err
}

func TestAudienceUsesPersonalMAXBindingsWithoutChannel(t *testing.T) {
	owner, member := uuid.New(), uuid.New()
	for _, role := range []domain.Role{domain.RoleAdvertiser, domain.RoleBlogger} {
		t.Run(string(role), func(t *testing.T) {
			r := boundRecipients{members: []uuid.UUID{owner, member}, max: map[uuid.UUID]int64{owner: 41, member: 42}}
			s := &Service{Deps: Deps{Recipients: r}}
			a, err := s.audience(context.Background(), domain.Notice{OrganizationID: uuid.New(), Role: role})
			if err != nil || len(a.MAXUsers) != 2 || a.MAXUsers[owner] != 41 || a.MAXUsers[member] != 42 {
				t.Fatalf("audience %+v, error %v", a, err)
			}
			if a.MaxUserID != nil {
				t.Fatal("personal binding used legacy channel recipient")
			}
		})
	}
}

func TestAudienceDoesNotRestoreChannelRecipientAfterDisconnect(t *testing.T) {
	user := uuid.New()
	s := &Service{Deps: Deps{Recipients: boundRecipients{members: []uuid.UUID{user}}}}
	a, err := s.audience(context.Background(), domain.Notice{OrganizationID: uuid.New(), Role: domain.RoleBlogger, Data: map[string]any{domain.DataChannelID: uuid.New()}})
	if err != nil || a.MAXUsers == nil || len(a.MAXUsers) != 0 || a.MaxUserID != nil {
		t.Fatalf("disconnected audience %+v, error %v", a, err)
	}
}

func TestQueuedMAXNotificationRequiresSamePersonalBinding(t *testing.T) {
	user := uuid.New()
	target := int64(41)
	n := domain.Notification{UserID: user, Channel: domain.ChannelMaxBot, MaxUserID: &target, Payload: map[string]any{"_max_bot_binding": true}}
	for _, tc := range []struct {
		name    string
		current int64
		want    bool
	}{{"connected", 41, true}, {"disconnected", 0, false}, {"replaced", 42, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Deps: Deps{Recipients: boundRecipients{max: map[uuid.UUID]int64{user: tc.current}}}}
			allowed, err := s.maxBindingCurrent(context.Background(), n)
			if err != nil || allowed != tc.want {
				t.Fatalf("allowed %v, error %v", allowed, err)
			}
		})
	}
	sentinel := errors.New("lookup failed")
	s := &Service{Deps: Deps{Recipients: boundRecipients{err: sentinel}}}
	if _, err := s.maxBindingCurrent(context.Background(), n); !errors.Is(err, sentinel) {
		t.Fatalf("lookup error %v", err)
	}
}

type directTransaction struct{}

func (directTransaction) InTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type queuedMAXRepository struct {
	domain.Repository
	row domain.Notification
}

func (r *queuedMAXRepository) GetForUpdate(context.Context, uuid.UUID) (domain.Notification, error) {
	return r.row, nil
}
func (r *queuedMAXRepository) Update(_ context.Context, n domain.Notification) error {
	r.row = n
	return nil
}

func TestSendSuppressesDisconnectedPersonalMAXWithoutDelivery(t *testing.T) {
	user := uuid.New()
	target := int64(41)
	repo := &queuedMAXRepository{row: domain.Notification{ID: uuid.New(), UserID: user, Channel: domain.ChannelMaxBot, MaxUserID: &target, Status: domain.StatusPending, Payload: map[string]any{"_max_bot_binding": true}}}
	s := New(Deps{Tx: directTransaction{}, Repo: repo, Recipients: boundRecipients{}, Clock: clock.NewManual(time.Now()), Log: logx.Discard()})
	// No delivery adapters are installed: disconnect must be handled before attempting a send.
	if err := s.Send(context.Background(), repo.row.ID); err != nil {
		t.Fatal(err)
	}
	if repo.row.Status != domain.StatusSuppressed || repo.row.Attempts != 0 || repo.row.LastError == nil || *repo.row.LastError != "max_bot_disconnected" {
		t.Fatalf("disconnected message %+v", repo.row)
	}
}
