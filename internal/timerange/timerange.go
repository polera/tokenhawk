// Package timerange resolves the window bounds shared by the spend view and
// the headless export filters, so a value accepted by one is accepted by both.
package timerange

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Preset is one relative window the spend view cycles through.
type Preset struct{ Spec, Label string }

// Presets are the built-in spend windows, in cycle order.
var Presets = []Preset{
	{"24h", "last 24 hours"},
	{"7d", "last 7 days"},
	{"30d", "last 30 days"},
	{"mtd", "month to date"},
	{"all", "all time"},
}

var errNotRelative = errors.New("not a relative offset")

// Parse resolves value against now. Accepted forms are RFC 3339 timestamps,
// YYYY-MM-DD dates, relative offsets such as 90m, 12h, 7d, 2w, 3mo, 1y, or any
// Go duration, and the keywords today, yesterday, wtd, mtd, ytd, and all. An
// empty value or "all" resolves to the zero time, meaning unbounded. Day-
// granular values resolve to the last instant of that day when endOfDay is set,
// which is what an inclusive --until bound needs.
func Parse(value string, now time.Time, endOfDay bool) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	switch lower {
	case "", "all":
		return time.Time{}, nil
	case "now":
		return now, nil
	case "today":
		return day(startOfDay(now), endOfDay), nil
	case "yesterday":
		return day(startOfDay(now).AddDate(0, 0, -1), endOfDay), nil
	case "wtd", "week":
		return startOfWeek(now), nil
	case "mtd", "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), nil
	case "ytd", "year":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()), nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", lower, now.Location()); err == nil {
		return day(t, endOfDay), nil
	}
	if t, err := offset(lower, now); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339, YYYY-MM-DD, a relative offset such as 7d or 3mo, or today, yesterday, wtd, mtd, ytd, all")
}

// Label names spec for display, preferring the preset wording.
func Label(spec string) string {
	lower := strings.ToLower(strings.TrimSpace(spec))
	for _, p := range Presets {
		if p.Spec == lower {
			return p.Label
		}
	}
	switch lower {
	case "", "all":
		return "all time"
	case "today", "yesterday":
		return lower
	case "wtd", "week":
		return "week to date"
	case "month":
		return "month to date"
	case "ytd", "year":
		return "year to date"
	}
	if _, err := offset(lower, time.Time{}); err == nil {
		return "last " + lower
	}
	return "since " + spec
}

// Next returns the preset following spec in cycle order. An unrecognized spec,
// such as one the user typed, cycles back to the first preset.
func Next(spec string) string {
	for i, p := range Presets {
		if p.Spec == strings.ToLower(strings.TrimSpace(spec)) {
			return Presets[(i+1)%len(Presets)].Spec
		}
	}
	return Presets[0].Spec
}

func offset(v string, now time.Time) (time.Time, error) {
	v = strings.TrimPrefix(v, "-")
	digits := 0
	for digits < len(v) && v[digits] >= '0' && v[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return time.Time{}, errNotRelative
	}
	n, err := strconv.Atoi(v[:digits])
	if err != nil {
		return time.Time{}, errNotRelative
	}
	switch v[digits:] {
	case "m", "min", "mins", "minute", "minutes":
		return now.Add(-time.Duration(n) * time.Minute), nil
	case "h", "hr", "hrs", "hour", "hours":
		return now.Add(-time.Duration(n) * time.Hour), nil
	case "d", "day", "days":
		return now.AddDate(0, 0, -n), nil
	case "w", "wk", "week", "weeks":
		return now.AddDate(0, 0, -7*n), nil
	case "mo", "mon", "month", "months":
		return now.AddDate(0, -n, 0), nil
	case "y", "yr", "year", "years":
		return now.AddDate(-n, 0, 0), nil
	}
	// Compound Go durations such as 1h30m fall through the single-unit table.
	if d, parseErr := time.ParseDuration(v); parseErr == nil {
		return now.Add(-d), nil
	}
	return time.Time{}, errNotRelative
}

func day(t time.Time, endOfDay bool) time.Time {
	if endOfDay {
		return t.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	return t
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func startOfWeek(t time.Time) time.Time {
	start := startOfDay(t)
	return start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
}
