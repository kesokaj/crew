package settings_test

import (
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/settings"
)

// machine_care.restart_hours (16.10): a range of the day in CREW_TIMEZONE, From in it and To not, past
// midnight when To is before From, and the same wall-clock hours on a day of summer time's change.
func TestRestartHours(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	day := func(d, hh, mm int) time.Time { return time.Date(2026, 10, d, hh, mm, 0, 0, loc) }
	for _, c := range []struct {
		hours string
		at    time.Time
		want  bool
	}{
		{"02:00-05:00", day(20, 2, 0), true},
		{"02:00-05:00", day(20, 4, 59), true},
		{"02:00-05:00", day(20, 5, 0), false},
		{"02:00-05:00", day(20, 1, 59), false},
		{"22:30-01:00", day(20, 23, 0), true},
		{"22:30-01:00", day(20, 0, 30), true},
		{"22:30-01:00", day(20, 1, 0), false},
		{"22:30-01:00", day(20, 22, 29), false},
		// 25 October 2026 has 25 hours in Stockholm: 03:00 comes twice, 02:00 to 05:00 still holds.
		{"02:00-05:00", day(25, 4, 0), true},
	} {
		h, err := settings.ParseHours(c.hours)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Holds(c.at, loc); got != c.want {
			t.Errorf("%s at %s: %v", c.hours, c.at.Format("Jan 2 15:04"), got)
		}
	}
	// The same instant read in UTC is another hour of the day: the zone decides. 02:00 in Stockholm
	// is 00:00 UTC on 20 October.
	if h, _ := settings.ParseHours("02:00-05:00"); h.Holds(day(20, 2, 0), time.UTC) {
		t.Error("02:00 in Stockholm holds in UTC too")
	}
	for _, bad := range []string{"", "2-5", "02:00-5:00", "24:00-01:00", "02:60-03:00", "03:00-03:00", " 02:00-05:00"} {
		if _, err := settings.ParseHours(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

// LastEnd (18.2): the daily mail goes when quiet hours end. The end is To on the same day, or on the day
// before when To is still to come; past midnight and on a day of 25 hours it is the wall clock's To.
func TestLastEnd(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	at := func(m time.Month, d, hh, mm int) time.Time { return time.Date(2026, m, d, hh, mm, 0, 0, loc) }
	for _, c := range []struct {
		hours    string
		now, end time.Time
	}{
		{"22:00-07:00", at(10, 20, 7, 0), at(10, 20, 7, 0)},
		{"22:00-07:00", at(10, 20, 12, 0), at(10, 20, 7, 0)},
		{"22:00-07:00", at(10, 20, 6, 59), at(10, 19, 7, 0)},
		{"22:00-07:00", at(10, 20, 23, 0), at(10, 20, 7, 0)},
		{"01:00-05:30", at(10, 20, 3, 0), at(10, 19, 5, 30)},
		// 25 October 2026 has 25 hours in Stockholm, 29 March 23: the end is still 07:00 on the clock.
		{"22:00-07:00", at(10, 25, 8, 0), at(10, 25, 7, 0)},
		{"22:00-07:00", at(3, 29, 8, 0), at(3, 29, 7, 0)},
		{"22:00-07:00", at(3, 29, 6, 0), at(3, 28, 7, 0)},
	} {
		h, err := settings.ParseHours(c.hours)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.LastEnd(c.now, loc); !got.Equal(c.end) {
			t.Errorf("%s at %s: %s, want %s", c.hours, c.now.Format("Jan 2 15:04"), got.Format("Jan 2 15:04 MST"), c.end.Format("Jan 2 15:04 MST"))
		}
	}
	// The zone decides: 08:00 in Stockholm is 06:00 UTC in summer time, before 07:00 UTC.
	h, _ := settings.ParseHours("22:00-07:00")
	if got := h.LastEnd(at(10, 20, 8, 0), time.UTC); !got.Equal(time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("in UTC: %s", got)
	}
}
