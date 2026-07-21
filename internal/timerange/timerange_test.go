package timerange

import (
	"testing"
	"time"
)

func TestParseAbsoluteRelativeAndKeywordWindows(t *testing.T) {
	now := time.Date(2026, 7, 20, 14, 30, 0, 0, time.Local)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{"2026-07-01T08:00:00Z", time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)},
		{"2026-07-01", time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)},
		{"90m", now.Add(-90 * time.Minute)},
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", time.Date(2026, 7, 13, 14, 30, 0, 0, time.Local)},
		{"2w", time.Date(2026, 7, 6, 14, 30, 0, 0, time.Local)},
		{"3mo", time.Date(2026, 4, 20, 14, 30, 0, 0, time.Local)},
		{"1y", time.Date(2025, 7, 20, 14, 30, 0, 0, time.Local)},
		{"1h30m", now.Add(-90 * time.Minute)},
		{"-7d", time.Date(2026, 7, 13, 14, 30, 0, 0, time.Local)},
		{"today", time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)},
		{"yesterday", time.Date(2026, 7, 19, 0, 0, 0, 0, time.Local)},
		{"wtd", time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)},
		{"mtd", time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)},
		{"ytd", time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)},
		{"MTD", time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)},
		{"all", time.Time{}},
		{"", time.Time{}},
	} {
		got, err := Parse(tc.value, now, false)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tc.value, err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("Parse(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
}

func TestParseRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"last tuesday", "7q", "2026-13-01", "d"} {
		if _, err := Parse(value, time.Now(), false); err == nil {
			t.Fatalf("Parse(%q) accepted an unusable window", value)
		}
	}
}

func TestEndOfDayBoundIsInclusive(t *testing.T) {
	now := time.Date(2026, 7, 20, 14, 30, 0, 0, time.Local)
	got, err := Parse("2026-07-01", now, true)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 2, 0, 0, 0, 0, time.Local).Add(-time.Nanosecond)
	if !got.Equal(want) {
		t.Fatalf("--until date = %s, want the last instant of the day %s", got, want)
	}
	// A timestamp already carries its own precision and must not be extended.
	got, err = Parse("2026-07-01T08:00:00Z", now, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("--until timestamp was rounded to a day: %s", got)
	}
}

func TestWeekToDateStartsMonday(t *testing.T) {
	for _, tc := range []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, 7, 20, 9, 0, 0, 0, time.Local), time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)},
		{time.Date(2026, 7, 19, 9, 0, 0, 0, time.Local), time.Date(2026, 7, 13, 0, 0, 0, 0, time.Local)},
		{time.Date(2026, 7, 23, 9, 0, 0, 0, time.Local), time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)},
	} {
		got, err := Parse("wtd", tc.now, false)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("wtd on %s = %s, want %s", tc.now.Weekday(), got, tc.want)
		}
	}
}

func TestPresetCycleWrapsAndLabels(t *testing.T) {
	spec := Presets[0].Spec
	seen := []string{spec}
	for range Presets[1:] {
		spec = Next(spec)
		seen = append(seen, spec)
	}
	if got := Next(spec); got != Presets[0].Spec {
		t.Fatalf("cycle did not wrap: %q -> %q", spec, got)
	}
	if got := Next("2026-07-01"); got != Presets[0].Spec {
		t.Fatalf("typed window did not rejoin the cycle: %q", got)
	}
	for i, s := range seen {
		if s != Presets[i].Spec {
			t.Fatalf("cycle order changed at %d: %v", i, seen)
		}
	}
	for _, tc := range []struct{ spec, want string }{
		{"7d", "last 7 days"},
		{"all", "all time"},
		{"3mo", "last 3mo"},
		{"2026-07-01", "since 2026-07-01"},
	} {
		if got := Label(tc.spec); got != tc.want {
			t.Fatalf("Label(%q) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}
