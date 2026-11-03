package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// testPlacement — размещение формата 1/24 с окном testWindow в состоянии status.
func testPlacement(status Status) *Placement {
	w := testWindow()
	return &Placement{
		ID: uuid.New(), OrderID: uuid.New(), ChannelID: uuid.New(), BloggerOrgID: uuid.New(), PostVersionID: uuid.New(),
		FormatID: "f_1_24", TopHours: 1, DurationHours: 24,
		WindowStart: w.Start, WindowEnd: w.End, TimeFrom: w.TimeFrom, TimeTo: w.TimeTo, Status: status, LockVersion: 1,
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestCheckPublishAt(t *testing.T) {
	now := mskTime(2026, 10, 20, 8, 0)
	busy := []Interval{TopInterval(mskTime(2026, 10, 20, 13, 0), 1), {Start: mskTime(2026, 10, 20, 15, 4), End: mskTime(2026, 10, 20, 16, 4)}}
	unavailable := []Interval{{Start: mskTime(2026, 10, 20, 10, 0), End: mskTime(2026, 10, 20, 12, 0)}}
	tests := []struct {
		name string
		at   *time.Time
		want error
	}{
		{"no publish_at", nil, ErrPublishAtRequired},
		{"before window", ptrTime(mskTime(2026, 10, 19, 23, 0)), ErrOutsideWindow},
		{"after window", ptrTime(mskTime(2026, 10, 23, 10, 0)), ErrOutsideWindow},
		{"before time_from", ptrTime(mskTime(2026, 10, 21, 8, 55)), ErrOutsideTime},
		{"at time_to", ptrTime(mskTime(2026, 10, 21, 21, 0)), ErrOutsideTime},
		{"exactly 1 h ahead", ptrTime(mskTime(2026, 10, 20, 9, 0)), nil},
		{"59 min ahead", ptrTime(mskTime(2026, 10, 20, 8, 59)), ErrOutsideTime},
		{"09:05 aligned", ptrTime(mskTime(2026, 10, 20, 9, 5)), nil},
		{"09:07 not aligned", ptrTime(mskTime(2026, 10, 20, 9, 7)), ErrNotAligned},
		{"seconds not aligned", ptrTime(mskTime(2026, 10, 20, 9, 5).Add(time.Second)), ErrNotAligned},
		{"in unavailability", ptrTime(mskTime(2026, 10, 20, 11, 55)), ErrChannelUnavailableInterval},
		{"at unavailability end", ptrTime(mskTime(2026, 10, 20, 12, 0)), nil},
		{"top ends where busy top starts", ptrTime(mskTime(2026, 10, 20, 12, 0)), nil},
		{"top starts where busy top ends", ptrTime(mskTime(2026, 10, 20, 14, 0)), nil},
		{"overlap with busy top", ptrTime(mskTime(2026, 10, 20, 12, 30)), ErrSlotTaken},
		{"overlap by one minute", ptrTime(mskTime(2026, 10, 20, 14, 5)), ErrSlotTaken},
	}
	for _, tc := range tests {
		p := testPlacement(StatusAwaitingSchedule)
		pub, rem, err := CheckPublishAt(p, ModeAt, tc.at, now, busy, unavailable, DefaultTiming)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
			continue
		}
		if err == nil && (!pub.Equal(*tc.at) || !rem.Equal(tc.at.Add(24*time.Hour))) {
			t.Errorf("%s: publish %s remove %s", tc.name, pub, rem)
		}
	}
	// Правило часа: в 10:00 МСК слот 10:59 слишком близок, хотя внутри окна.
	p := testPlacement(StatusAwaitingSchedule)
	if _, _, err := CheckPublishAt(p, ModeAt, ptrTime(mskTime(2026, 10, 20, 10, 55)), mskTime(2026, 10, 20, 10, 0),
		nil, nil, DefaultTiming); !errors.Is(err, ErrTooSoon) {
		t.Errorf("55 min ahead: %v", err)
	}
	if _, _, err := CheckPublishAt(p, "later", nil, now, nil, nil, DefaultTiming); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("unknown mode: %v", err)
	}
}

func TestCheckPublishAtNow(t *testing.T) {
	p := testPlacement(StatusAwaitingSchedule)
	p.DurationHours = 48
	now := mskTime(2026, 10, 20, 10, 30).Add(15*time.Second + 300*time.Millisecond)
	pub, rem, err := CheckPublishAt(p, ModeNow, nil, now, nil, nil, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(now.Truncate(time.Second)) || !rem.Equal(pub.Add(48*time.Hour)) {
		t.Fatalf("now: publish %s remove %s", pub, rem)
	}
	// Правила часа и кратности к «сейчас» не применяются, недоступность и I-06 — применяются.
	if _, _, err := CheckPublishAt(p, ModeNow, nil, mskTime(2026, 10, 20, 22, 0), nil, nil, DefaultTiming); !errors.Is(err, ErrOutsideTime) {
		t.Errorf("now outside time: %v", err)
	}
	if _, _, err := CheckPublishAt(p, ModeNow, nil, mskTime(2026, 10, 23, 10, 0), nil, nil, DefaultTiming); !errors.Is(err, ErrOutsideWindow) {
		t.Errorf("now outside window: %v", err)
	}
	busy := []Interval{TopInterval(mskTime(2026, 10, 20, 10, 0), 1)}
	if _, _, err := CheckPublishAt(p, ModeNow, nil, now, busy, nil, DefaultTiming); !errors.Is(err, ErrSlotTaken) {
		t.Errorf("now inside busy top: %v", err)
	}
	unavail := []Interval{{Start: mskTime(2026, 10, 20, 10, 0), End: mskTime(2026, 10, 20, 11, 0)}}
	if _, _, err := CheckPublishAt(p, ModeNow, nil, now, nil, unavail, DefaultTiming); !errors.Is(err, ErrChannelUnavailableInterval) {
		t.Errorf("now inside unavailability: %v", err)
	}
}

func TestCanReschedule(t *testing.T) {
	now := mskTime(2026, 10, 20, 10, 0)
	p := testPlacement(StatusScheduled)
	p.PublishAt = ptrTime(now.Add(61 * time.Minute))
	if !p.CanReschedule(now, DefaultTiming) {
		t.Error("61 min before publish_at")
	}
	p.PublishAt = ptrTime(now.Add(59 * time.Minute))
	if p.CanReschedule(now, DefaultTiming) {
		t.Error("59 min before publish_at")
	}
	p.PublishAt = ptrTime(now.Add(60 * time.Minute))
	if p.CanReschedule(now, DefaultTiming) {
		t.Error("exactly 1 h: must be more than 1 h")
	}
	_, err := p.Reschedule(uuid.New(), now.Add(3*time.Hour), now.Add(27*time.Hour), now, DefaultTiming)
	if !apperr.Is(err, apperr.CodeInvalidTransition) || p.Status != StatusScheduled {
		t.Fatalf("late reschedule: %v", err)
	}
}
