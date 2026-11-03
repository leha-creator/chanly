package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/post/domain"
)

func newVersion(t *testing.T, body string, status domain.VersionStatus) domain.Version {
	t.Helper()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	post := domain.NewPost(uuid.New(), uuid.New(), uuid.New(), now)
	file := uuid.New()
	if err := post.SaveDraft(domain.DraftInput{Doc: doc(p(txt("Текст"))), MediaIDs: []uuid.UUID{file}}, 10, now); err != nil {
		t.Fatal(err)
	}
	v, err := domain.NewVersion(uuid.New(), post, 1, domain.Requisites{Name: "ООО Ромашка", INN: "7707083893"}, body,
		[]domain.VersionMedia{{Position: 0, MediaFileID: file, DerivativeID: uuid.New()}}, uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	v.Status = status
	return v
}

func TestNewVersion(t *testing.T) {
	v := newVersion(t, "Текст", domain.VersionPendingModeration)
	if v.Status != domain.VersionPendingModeration || v.TokenCarrier != domain.CarrierMedia || v.AdvertiserINN != "7707083893" || len(v.Media) != 1 {
		t.Fatalf("%+v", v)
	}
	post := domain.NewPost(uuid.New(), uuid.New(), uuid.New(), time.Now())
	if _, err := domain.NewVersion(uuid.New(), post, 1, domain.Requisites{Name: "", INN: "1"}, "x", nil, uuid.New(), time.Now()); err == nil {
		t.Fatal("version without requisites accepted")
	}
	if _, err := domain.NewVersion(uuid.New(), post, 0, domain.Requisites{Name: "a", INN: "1"}, "x", nil, uuid.New(), time.Now()); err == nil {
		t.Fatal("version number 0 accepted")
	}
}

func TestVersionTransitions(t *testing.T) {
	admin := uuid.New()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	transitions := map[string]func(*domain.Version) error{
		domain.TransitionApprove:   func(v *domain.Version) error { return v.Approve(admin, now) },
		domain.TransitionReject:    func(v *domain.Version) error { return v.Reject(admin, "Нарушает правила", now) },
		domain.TransitionMarkReady: func(v *domain.Version) error { return v.MarkReady("E1", domain.DefaultFinalTextLimit) },
	}
	allowed := map[domain.VersionStatus]map[string]domain.VersionStatus{
		domain.VersionPendingModeration: {domain.TransitionApprove: domain.VersionApproved, domain.TransitionReject: domain.VersionRejected},
		domain.VersionApproved:          {domain.TransitionMarkReady: domain.VersionReady},
		domain.VersionRejected:          {},
		domain.VersionReady:             {},
	}
	for from, ok := range allowed {
		for name, apply := range transitions {
			t.Run(string(from)+"/"+name, func(t *testing.T) {
				v := newVersion(t, "Текст", from)
				lock := v.LockVersion
				err := apply(&v)
				to, want := ok[name]
				if !want {
					if !apperr.Is(err, apperr.CodeInvalidTransition) || v.Status != from {
						t.Fatalf("want invalid_transition, got %v (status %s)", err, v.Status)
					}
					return
				}
				if err != nil || v.Status != to || v.LockVersion != lock+1 {
					t.Fatalf("got %v, status %s, lock %d", err, v.Status, v.LockVersion)
				}
			})
		}
	}
}

func TestVersionMarkReady(t *testing.T) {
	v := newVersion(t, "Текст", domain.VersionApproved)
	if err := v.MarkReady("2Vtzq", domain.DefaultFinalTextLimit); err != nil {
		t.Fatal(err)
	}
	if v.Erid != "2Vtzq" || v.FinalText != "Текст\n\nerid: 2Vtzq" {
		t.Fatalf("final text %q", v.FinalText)
	}

	long := newVersion(t, strings.Repeat("я", 3995), domain.VersionApproved)
	if err := long.MarkReady("2Vtzq", domain.DefaultFinalTextLimit); !errors.Is(err, domain.ErrFinalTextTooLong) {
		t.Fatalf("want ErrFinalTextTooLong, got %v", err)
	}
	if long.Status != domain.VersionApproved || long.FinalText != "" || long.Erid != "" {
		t.Fatalf("failed MarkReady changed version: %+v", long.Status)
	}
	if err := newVersionPtr(t).MarkReady(" ", 4000); !errors.Is(err, domain.ErrEridRequired) {
		t.Fatalf("empty erid: %v", err)
	}
}

func newVersionPtr(t *testing.T) *domain.Version {
	v := newVersion(t, "Текст", domain.VersionApproved)
	return &v
}

func TestVersionRejectReason(t *testing.T) {
	for _, reason := range []string{"", "   ", strings.Repeat("я", 1001)} {
		v := newVersion(t, "Текст", domain.VersionPendingModeration)
		if err := v.Reject(uuid.New(), reason, time.Now()); !errors.Is(err, domain.ErrRejectionReason) || v.Status != domain.VersionPendingModeration {
			t.Errorf("reason of %d chars: %v", len([]rune(reason)), err)
		}
	}
	v := newVersion(t, "Текст", domain.VersionPendingModeration)
	if err := v.Reject(uuid.New(), strings.Repeat("я", 1000), time.Now()); err != nil {
		t.Fatal(err)
	}
}
