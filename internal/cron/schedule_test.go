package cron

import (
	"strings"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseScheduleInvalid(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"blank":            "   ",
		"four fields":      "* * * *",
		"six fields":       "* * * * * *",
		"minute high":      "60 * * * *",
		"hour high":        "* 24 * * *",
		"dom zero":         "* * 0 * *",
		"dom high":         "* * 32 * *",
		"month zero":       "* * * 0 *",
		"month high":       "* * * 13 *",
		"dow high":         "* * * * 8",
		"zero step":        "*/0 * * * *",
		"negative":         "-1 * * * *",
		"plus sign":        "+5 * * * *",
		"backwards range":  "30-10 * * * *",
		"step without rng": "5/10 * * * *",
		"empty element":    "1,,2 * * * *",
		"trailing comma":   "1, * * * *",
		"bad name":         "* * * foo *",
		"name in minute":   "mon * * * *",
		"unknown macro":    "@sometimes",
		"reboot":           "@reboot",
		"question mark":    "? * * * *",
		"non-numeric step": "*/x * * * *",
		"empty range end":  "5- * * * *",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSchedule(spec); err == nil {
				t.Fatalf("ParseSchedule(%q) succeeded, want an error", spec)
			}
		})
	}
}

func TestParseScheduleMatches(t *testing.T) {
	// 2026-11-01 is a Sunday; 2026-11-13 is a Friday.
	cases := []struct {
		spec string
		at   string
		want bool
	}{
		{"* * * * *", "2026-11-17 10:23", true},
		{"5 * * * *", "2026-11-17 10:05", true},
		{"5 * * * *", "2026-11-17 10:06", false},
		{"*/15 * * * *", "2026-11-17 10:45", true},
		{"*/15 * * * *", "2026-11-17 10:46", false},
		{"10-20/5 * * * *", "2026-11-17 10:15", true},
		{"10-20/5 * * * *", "2026-11-17 10:16", false},
		{"10-20/5 * * * *", "2026-11-17 10:25", false},
		{"1,2,3 * * * *", "2026-11-17 10:02", true},
		{"1,2,3 * * * *", "2026-11-17 10:04", false},
		{"0 9-17 * * *", "2026-11-17 17:00", true},
		{"0 9-17 * * *", "2026-11-17 18:00", false},
		{"0 0 1 jan *", "2026-01-01 00:00", true},
		{"0 0 1 JAN *", "2026-01-01 00:00", true},
		{"0 0 1 jan-mar *", "2026-02-01 00:00", true},
		{"0 0 1 jan-mar *", "2026-04-01 00:00", false},
		{"0 0 * * sun", "2026-11-01 00:00", true},
		{"0 0 * * 0", "2026-11-01 00:00", true},
		{"0 0 * * 7", "2026-11-01 00:00", true},
		{"0 0 * * 5-7", "2026-11-01 00:00", true},
		{"0 0 * * mon-fri", "2026-11-13 00:00", true},
		{"0 0 * * mon-fri", "2026-11-14 00:00", false},
		{"*/10 8-18 * * 1-5", "2026-11-17 12:30", true},

		// Both day fields restricted: either may match.
		{"0 0 13 * fri", "2026-11-13 00:00", true}, // 13th and a Friday
		{"0 0 13 * fri", "2026-11-20 00:00", true}, // Friday only
		{"0 0 13 * fri", "2026-03-13 00:00", true}, // 13th only (a Friday too, but check Dec below)
		{"0 0 13 * fri", "2026-12-13 00:00", true}, // 13th, a Sunday
		{"0 0 13 * fri", "2026-11-14 00:00", false},
		// A '*' in either day field makes the other the only constraint.
		{"0 0 * * fri", "2026-11-13 00:00", true},
		{"0 0 * * fri", "2026-11-14 00:00", false},
		{"0 0 13 * *", "2026-12-13 00:00", true},
		{"0 0 13 * *", "2026-11-20 00:00", false},
		// '*/2' starts with '*', so it is "unrestricted" for the OR rule: AND with Friday.
		{"0 0 */2 * fri", "2026-11-13 00:00", true},  // odd day, Friday
		{"0 0 */2 * fri", "2026-11-20 00:00", false}, // even day, Friday
		{"0 0 */2 * fri", "2026-11-15 00:00", false}, // odd day, Sunday

		{"@hourly", "2026-11-17 10:00", true},
		{"@hourly", "2026-11-17 10:01", false},
		{"@daily", "2026-11-17 00:00", true},
		{"@daily", "2026-11-17 01:00", false},
		{"@midnight", "2026-11-17 00:00", true},
		{"@weekly", "2026-11-15 00:00", true},  // Sunday
		{"@weekly", "2026-11-16 00:00", false}, // Monday
		{"@monthly", "2026-11-01 00:00", true},
		{"@monthly", "2026-11-02 00:00", false},
		{"@yearly", "2026-01-01 00:00", true},
		{"@annually", "2026-02-01 00:00", false},
		{"  0   0  * * *  ", "2026-11-17 00:00", true},
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.spec)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", c.spec, err)
			continue
		}
		if got := s.Matches(ts(c.at)); got != c.want {
			t.Errorf("%q at %s: Matches = %v, want %v", c.spec, c.at, got, c.want)
		}
	}
}

func TestScheduleNext(t *testing.T) {
	cases := []struct {
		spec, after, want string
	}{
		{"* * * * *", "2026-11-17 10:00", "2026-11-17 10:01"},
		{"*/15 * * * *", "2026-11-17 10:15", "2026-11-17 10:30"},
		{"0 0 * * *", "2026-11-17 00:00", "2026-11-18 00:00"},
		{"30 2 * * *", "2026-11-17 02:31", "2026-11-18 02:30"},
		{"0 0 1 * *", "2026-11-17 10:00", "2026-12-01 00:00"},
		{"0 0 1 1 *", "2026-11-17 10:00", "2027-01-01 00:00"},
		{"0 12 * * mon", "2026-11-17 10:00", "2026-11-23 12:00"},
		{"0 0 29 2 *", "2026-11-17 10:00", "2028-02-29 00:00"},
		{"0 0 13 * fri", "2026-11-14 00:00", "2026-11-20 00:00"},
		{"@hourly", "2026-11-17 10:30", "2026-11-17 11:00"},
		{"59 23 31 12 *", "2026-12-31 23:58", "2026-12-31 23:59"},
		{"0 0 1 1 *", "2026-12-31 23:59", "2027-01-01 00:00"},
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.spec)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", c.spec, err)
		}
		got, ok := s.Next(ts(c.after))
		if !ok || !got.Equal(ts(c.want)) {
			t.Errorf("%q after %s: Next = %v (%v), want %s", c.spec, c.after, got, ok, c.want)
		}
	}
}

func TestScheduleNextIgnoresSeconds(t *testing.T) {
	s, _ := ParseSchedule("* * * * *")
	got, _ := s.Next(ts("2026-11-17 10:00").Add(45 * time.Second))
	if want := ts("2026-11-17 10:01"); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestScheduleNextNeverFires(t *testing.T) {
	for _, spec := range []string{"0 0 31 2 *", "0 0 30 feb *", "0 0 31 4 *"} {
		s, err := ParseSchedule(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := s.Next(ts("2026-11-17 10:00")); ok {
			t.Errorf("%q: Next = %v, want no firing", spec, got)
		}
	}
}

func TestScheduleString(t *testing.T) {
	s, _ := ParseSchedule("  @daily ")
	if s.String() != "@daily" {
		t.Fatalf("String = %q", s.String())
	}
	if _, err := ParseSchedule("* * * *"); err == nil || !strings.Contains(err.Error(), "5") {
		t.Fatalf("field-count error should say what is wanted, got %v", err)
	}
}
