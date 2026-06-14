package scheduler

import (
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		input string
		want  Schedule
		err   bool
	}{
		{"every day at 16", Schedule{Type: "daily", Hour: 16, Minute: 0}, false},
		{"every day at 16:30", Schedule{Type: "daily", Hour: 16, Minute: 30}, false},
		{"every day at 0", Schedule{Type: "daily", Hour: 0, Minute: 0}, false},
		{"once a week", Schedule{Type: "weekly", Weekday: time.Monday, Hour: 0, Minute: 0}, false},
		{"every monday", Schedule{Type: "weekly", Weekday: time.Monday, Hour: 0, Minute: 0}, false},
		{"every monday at 10", Schedule{Type: "weekly", Weekday: time.Monday, Hour: 10, Minute: 0}, false},
		{"every friday at 18:45", Schedule{Type: "weekly", Weekday: time.Friday, Hour: 18, Minute: 45}, false},
		{"every hour", Schedule{Type: "interval", Interval: time.Hour}, false},
		{"every 3 hours", Schedule{Type: "interval", Interval: 3 * time.Hour}, false},
		{"every three hours", Schedule{Type: "interval", Interval: 3 * time.Hour}, false},
		{"every six hours", Schedule{Type: "interval", Interval: 6 * time.Hour}, false},
		{"every 24 hours", Schedule{Type: "interval", Interval: 24 * time.Hour}, false},
		{"", Schedule{}, true},
		{"garbage input", Schedule{}, true},
	}

	for _, tt := range tests {
		got, err := ParseSchedule(tt.input)
		if tt.err {
			if err == nil {
				t.Errorf("ParseSchedule(%q) expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSchedule(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got.Type != tt.want.Type || got.Hour != tt.want.Hour || got.Minute != tt.want.Minute ||
			got.Weekday != tt.want.Weekday || got.Interval != tt.want.Interval {
			t.Errorf("ParseSchedule(%q) = %+v, want %+v", tt.input, got, tt.want)
		}
	}
}

func TestNextAfter(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC) // Friday

	tests := []struct {
		sched Schedule
		after time.Time
		want  time.Time
	}{
		{daily(16, 0), now, time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)},
		{daily(8, 0), now, time.Date(2026, 6, 13, 8, 0, 0, 0, time.UTC)},
		{daily(10, 0), now, time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)}, // same hour, not after → next day
		{weekly(time.Monday, 0, 0), now, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		{weekly(time.Friday, 0, 0), now, time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)}, // next Friday (today is Friday, 10:00, so 00:00 was earlier)
		{interval(3 * time.Hour), now, time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC)},
		{interval(time.Hour), now, time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		got := tt.sched.NextAfter(tt.after)
		if !got.Equal(tt.want) {
			t.Errorf("%+v.NextAfter(%v) = %v, want %v", tt.sched, tt.after, got, tt.want)
		}
	}
}

func daily(hour, min int) Schedule {
	return Schedule{Type: "daily", Hour: hour, Minute: min}
}

func weekly(day time.Weekday, hour, min int) Schedule {
	return Schedule{Type: "weekly", Weekday: day, Hour: hour, Minute: min}
}

func interval(d time.Duration) Schedule {
	return Schedule{Type: "interval", Interval: d}
}
