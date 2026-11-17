package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression: five fields (minute, hour, day of month,
// month, day of week) or one of the @hourly-style macros.
//
// Day of month and day of week combine the way Vixie cron does: when both fields
// are restricted (neither starts with '*') a day matches if either matches; when
// at least one starts with '*' both must match, which is the same as the
// restricted one alone.
type Schedule struct {
	spec    string
	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
}

var macros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// ParseSchedule parses a cron expression. @reboot and seconds fields are not
// supported.
func ParseSchedule(spec string) (Schedule, error) {
	orig := spec
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Schedule{}, errors.New("cron: schedule is empty")
	}
	if strings.HasPrefix(spec, "@") {
		expanded, ok := macros[strings.ToLower(spec)]
		if !ok {
			return Schedule{}, fmt.Errorf("cron: unknown macro %q (want @hourly, @daily, @weekly, @monthly or @yearly)", spec)
		}
		spec = expanded
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron: schedule %q has %d fields, want 5", orig, len(fields))
	}

	s := Schedule{spec: strings.TrimSpace(orig)}
	var err error
	if s.minute, _, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: minute: %w", err)
	}
	if s.hour, _, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: hour: %w", err)
	}
	if s.dom, s.domStar, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("cron: day of month: %w", err)
	}
	if s.month, _, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("cron: month: %w", err)
	}
	if s.dow, s.dowStar, err = parseField(fields[4], 0, 7, dowNames); err != nil {
		return Schedule{}, fmt.Errorf("cron: day of week: %w", err)
	}
	// Sunday is both 0 and 7.
	if s.dow&(1<<7) != 0 {
		s.dow |= 1
	}
	s.dow &^= 1 << 7
	return s, nil
}

// String returns the expression as it was given.
func (s Schedule) String() string { return s.spec }

func parseField(field string, lo, hi int, names map[string]int) (mask uint64, star bool, err error) {
	if field == "" {
		return 0, false, errors.New("empty field")
	}
	star = strings.HasPrefix(field, "*")
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("empty list element in %q", field)
		}
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			step, err = strconv.Atoi(stepPart)
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("step %q must be a positive number", stepPart)
			}
		}

		var from, to int
		switch {
		case rangePart == "*":
			from, to = lo, hi
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			if from, err = parseValue(a, lo, hi, names); err != nil {
				return 0, false, err
			}
			if to, err = parseValue(b, lo, hi, names); err != nil {
				return 0, false, err
			}
			if from > to {
				return 0, false, fmt.Errorf("range %q runs backwards", rangePart)
			}
		default:
			if hasStep {
				return 0, false, fmt.Errorf("%q: a step needs '*' or a range", part)
			}
			if from, err = parseValue(rangePart, lo, hi, names); err != nil {
				return 0, false, err
			}
			to = from
		}
		for v := from; v <= to; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, star, nil
}

func parseValue(s string, lo, hi int, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("%q is not a number or name", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is outside %d-%d", v, lo, hi)
	}
	return v, nil
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// Matches reports whether t, read in its own location and truncated to the
// minute, is a time the schedule fires.
func (s Schedule) Matches(t time.Time) bool {
	return s.minute&(1<<uint(t.Minute())) != 0 &&
		s.hour&(1<<uint(t.Hour())) != 0 &&
		s.month&(1<<uint(t.Month())) != 0 &&
		s.dayMatches(t)
}

// maxLookahead bounds Next so an impossible date such as 31 February returns
// instead of looping. Eight years covers a leap day that falls on a given weekday.
const maxLookahead = 8

// Next returns the first firing time strictly after t, in t's location. The bool
// is false when the schedule never fires, for example "0 0 31 2 *".
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	loc := after.Location()
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(maxLookahead, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}
