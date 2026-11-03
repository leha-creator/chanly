package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func ptr[T any](v T) *T { return &v }

func tod(s string) *domain.TimeOfDay {
	t, err := domain.ParseTimeOfDay(s)
	if err != nil {
		panic(err)
	}
	return &t
}

func date(y int, m time.Month, d int) *domain.Date { return ptr(domain.NewDate(y, m, d)) }

func reasonOf(err error) string {
	var re *domain.RuleError
	if errors.As(err, &re) {
		return re.Reason
	}
	return ""
}

func TestNewCampaignRules(t *testing.T) {
	// 2026-10-16 23:59 по Москве = 20:59 UTC; 2026-10-17 00:00 по Москве = 21:00 UTC.
	beforeMidnight := time.Date(2026, 10, 16, 20, 59, 0, 0, time.UTC)
	afterMidnight := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	name := ptr("Осенняя распродажа")
	tests := []struct {
		name   string
		in     domain.CampaignInput
		now    time.Time
		reason string
	}{
		{"only name", domain.CampaignInput{Name: name}, beforeMidnight, ""},
		{"no name", domain.CampaignInput{}, beforeMidnight, domain.ReasonRequired},
		{"blank name", domain.CampaignInput{Name: ptr("   ")}, beforeMidnight, domain.ReasonRequired},
		{"name 200", domain.CampaignInput{Name: ptr(strings.Repeat("я", 200))}, beforeMidnight, ""},
		{"name 201", domain.CampaignInput{Name: ptr(strings.Repeat("я", 201))}, beforeMidnight, domain.ReasonTooLong},
		{"one day", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 20), DateTo: date(2026, 10, 20)}, beforeMidnight, ""},
		{"14 days", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 20), DateTo: date(2026, 11, 2)}, beforeMidnight, ""},
		{"15 days", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 20), DateTo: date(2026, 11, 3)}, beforeMidnight, domain.ReasonPeriodTooLong},
		{"reversed", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 21), DateTo: date(2026, 10, 20)}, beforeMidnight, domain.ReasonPeriodOrder},
		{"today at 23:59 MSK", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 16), DateTo: date(2026, 10, 17)}, beforeMidnight, ""},
		{"yesterday after midnight MSK", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 16), DateTo: date(2026, 10, 17)}, afterMidnight, domain.ReasonPeriodInPast},
		{"today after midnight MSK", domain.CampaignInput{Name: name, DateFrom: date(2026, 10, 17)}, afterMidnight, ""},
		{"window 1h", domain.CampaignInput{Name: name, TimeFrom: tod("10:00"), TimeTo: tod("11:00")}, beforeMidnight, ""},
		{"window 59m", domain.CampaignInput{Name: name, TimeFrom: tod("10:00"), TimeTo: tod("10:59")}, beforeMidnight, domain.ReasonTimeWindow},
		{"window reversed", domain.CampaignInput{Name: name, TimeFrom: tod("20:00"), TimeTo: tod("10:00")}, beforeMidnight, domain.ReasonTimeWindow},
		{"only time_from", domain.CampaignInput{Name: name, TimeFrom: tod("10:00")}, beforeMidnight, domain.ReasonTimePair},
		{"only time_to", domain.CampaignInput{Name: name, TimeTo: tod("10:00")}, beforeMidnight, domain.ReasonTimePair},
		{"requirements 2000", domain.CampaignInput{Name: name, ExtraRequirements: ptr(strings.Repeat("я", 2000))}, beforeMidnight, ""},
		{"requirements 2001", domain.CampaignInput{Name: name, ExtraRequirements: ptr(strings.Repeat("я", 2001))}, beforeMidnight, domain.ReasonTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := domain.NewCampaign(uuid.New(), uuid.New(), uuid.New(), tt.in, tt.now)
			if got := reasonOf(err); got != tt.reason || (tt.reason == "" && err != nil) {
				t.Fatalf("reason %q (%v), want %q", got, err, tt.reason)
			}
			if err == nil && (c.Status != domain.CampaignDraft || c.Name != strings.TrimSpace(*tt.in.Name)) {
				t.Fatalf("campaign %+v", c)
			}
		})
	}
}

func TestCampaignUpdate(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	c, err := domain.NewCampaign(uuid.New(), uuid.New(), uuid.New(), domain.CampaignInput{
		Name: ptr("К"), DateFrom: date(2026, 10, 20), DateTo: date(2026, 10, 25), TimeFrom: tod("10:00"), TimeTo: tod("20:00"),
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Hour)
	changed, err := c.Update(domain.CampaignInput{Name: ptr("Новое"), DateTo: date(2026, 10, 22)}, later)
	if err != nil || len(changed) != 2 || c.Name != "Новое" || !c.DateTo.Equal(*date(2026, 10, 22)) || !c.UpdatedAt.Equal(later) {
		t.Fatalf("update: %v %v %+v", changed, err, c)
	}
	// Неизменённые поля остаются; ошибка не портит кампанию.
	if _, err := c.Update(domain.CampaignInput{TimeTo: tod("10:30")}, later); reasonOf(err) != domain.ReasonTimeWindow {
		t.Fatalf("window: %v", err)
	}
	if c.TimeTo.String() != "20:00" {
		t.Fatalf("failed update changed campaign: %v", c.TimeTo)
	}
	// Переименование, когда даты уже в прошлом, допустимо.
	weekLater := now.AddDate(0, 0, 10)
	if _, err := c.Update(domain.CampaignInput{Name: ptr("Ещё")}, weekLater); err != nil {
		t.Fatalf("rename with past dates: %v", err)
	}
	if _, err := c.Update(domain.CampaignInput{DateTo: date(2026, 10, 27)}, weekLater); reasonOf(err) != domain.ReasonPeriodInPast {
		t.Fatalf("change past period: %v", err)
	}

	// Вне draft: название — можно, период и требования — ErrNotDraft.
	c.Status = domain.CampaignOrdered
	if _, err := c.Update(domain.CampaignInput{Name: ptr("Заказанная")}, later); err != nil {
		t.Fatalf("rename ordered: %v", err)
	}
	for _, in := range []domain.CampaignInput{
		{DateFrom: date(2026, 10, 21)}, {TimeFrom: tod("11:00")}, {ExtraRequirements: ptr("x")},
	} {
		if _, err := c.Update(in, later); !errors.Is(err, domain.ErrNotDraft) {
			t.Errorf("%+v: want ErrNotDraft, got %v", in, err)
		}
	}
}

func TestTimeOfDay(t *testing.T) {
	v, err := domain.ParseTimeOfDay("09:05")
	if err != nil || int(v) != 545 || v.String() != "09:05" {
		t.Fatalf("%v %v", v, err)
	}
	if _, err := domain.ParseTimeOfDay("25:00"); err == nil {
		t.Fatal("25:00 accepted")
	}
}
