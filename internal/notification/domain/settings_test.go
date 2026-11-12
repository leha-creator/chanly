package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestSettingsDefaultsRightsAndMandatory(t *testing.T) {
	s := DefaultSettings()
	for _, tc := range []struct {
		roles []Role
		max   bool
	}{{nil, false}, {[]Role{RoleAdvertiser}, false}, {[]Role{RoleBlogger}, false}, {[]Role{RoleBlogger}, true}, {[]Role{RoleAdmin}, false}} {
		a := SettingsAccess{Roles: tc.roles, MaxBot: tc.max}
		for _, o := range s.Catalog(a) {
			if o.Channel == "max_bot" && o.Available && (!tc.max || !strings.HasPrefix(o.Group, "blogger.")) {
				t.Fatal("MAX leaked")
			}
			if (o.Channel == "browser" || o.Channel == "sound") && o.Enabled {
				t.Fatal("browser signal on by default")
			}
			if o.Mandatory && (!o.Enabled || o.Reason == "") {
				t.Fatal("missing mandatory metadata")
			}
		}
	}
	a := SettingsAccess{Roles: []Role{RoleAdvertiser}}
	s, err := s.Apply(SettingsPatch{Version: 0, Delivery: []Preference{{"advertiser.disputes", "*", "email", false}}}, a)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled("advertiser.disputes", "placement.disputed", "email") {
		t.Fatal("group disables mandatory event")
	}
	_, err = s.Apply(SettingsPatch{Version: 1, Delivery: []Preference{{"advertiser.disputes", "placement.disputed", "email", false}}}, a)
	if !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatal(err)
	}
	_, err = s.Apply(SettingsPatch{Version: 0}, a)
	if !apperr.Is(err, apperr.CodeConflict) {
		t.Fatal(err)
	}
	_, err = s.Apply(SettingsPatch{Version: 1, Delivery: []Preference{{"blogger.requests", "*", "email", false}}}, a)
	if !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatal(err)
	}
}
func TestQuietHoursBoundariesMSK(t *testing.T) {
	for _, tc := range []struct {
		start, end, at string
		delay          time.Duration
	}{
		{"22:00", "08:00", "2026-11-12T18:59:59Z", 0},
		{"22:00", "08:00", "2026-11-12T19:00:00Z", 10 * time.Hour},
		{"22:00", "08:00", "2026-11-13T04:59:59Z", time.Second},
		{"22:00", "08:00", "2026-11-13T05:00:00Z", 0},
		{"09:00", "18:00", "2026-11-12T06:00:00Z", 9 * time.Hour},
		{"09:00", "18:00", "2026-11-12T15:00:00Z", 0},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		q := QuietHours{Enabled: true, Start: tc.start, End: tc.end, Timezone: "Europe/Moscow"}
		if got := q.QuietDelay(at); got != tc.delay {
			t.Fatalf("%+v got %s", tc, got)
		}
	}
}
