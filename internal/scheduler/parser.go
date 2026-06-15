package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule describes when a sync should run automatically.
type Schedule struct {
	Type     string        // "daily", "weekly", "interval"
	Hour     int           // 0-23 (daily/weekly)
	Minute   int           // 0-59 (daily/weekly)
	Weekday  time.Weekday  // (weekly)
	Interval time.Duration // (interval)
}

// NextAfter returns the next run time after the given time.
func (s Schedule) NextAfter(after time.Time) time.Time {
	switch s.Type {
	case "daily":
		next := time.Date(after.Year(), after.Month(), after.Day(), s.Hour, s.Minute, 0, 0, after.Location())
		if !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
		return next

	case "weekly":
		next := time.Date(after.Year(), after.Month(), after.Day(), s.Hour, s.Minute, 0, 0, after.Location())
		for next.Weekday() != s.Weekday || !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
		return next

	case "interval":
		return after.Add(s.Interval)

	default:
		return after
	}
}

// ParseSchedule parses a plain-text schedule string.
// Supported formats:
//
//	"every day at HH" or "every day at HH:MM"
//	"once a week"
//	"every <weekday>" or "every <weekday> at HH" or "every <weekday> at HH:MM"
//	"every hour"
//	"every N hours"
func ParseSchedule(text string) (Schedule, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Schedule{}, fmt.Errorf("empty schedule")
	}
	lower := strings.ToLower(text)

	// "every day at HH:MM" or "every day at HH"
	if strings.HasPrefix(lower, "every day at ") {
		t := strings.TrimSpace(text[len("every day at "):])
		h, m, err := parseTime(t)
		if err != nil {
			return Schedule{}, fmt.Errorf("parse time %q: %w", t, err)
		}
		return Schedule{Type: "daily", Hour: h, Minute: m}, nil
	}
	if lower == "every day" {
		return Schedule{Type: "daily", Hour: 0, Minute: 0}, nil
	}

	// "once a week"
	if lower == "once a week" {
		return Schedule{Type: "weekly", Weekday: time.Monday, Hour: 0, Minute: 0}, nil
	}

	// "every <weekday> [at HH:MM]"
	weekdayNames := []string{
		"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
	}
	for i, name := range weekdayNames {
		prefix := "every " + name + " at "
		if strings.HasPrefix(lower, prefix) {
			t := strings.TrimSpace(text[len(prefix):])
			h, m, err := parseTime(t)
			if err != nil {
				return Schedule{}, fmt.Errorf("parse time %q: %w", t, err)
			}
			return Schedule{Type: "weekly", Weekday: time.Weekday(i), Hour: h, Minute: m}, nil
		}
		if lower == "every "+name {
			return Schedule{Type: "weekly", Weekday: time.Weekday(i), Hour: 0, Minute: 0}, nil
		}
	}

	// "every hour"
	if lower == "every hour" {
		return Schedule{Type: "interval", Interval: time.Hour}, nil
	}

	// "every N hours" — handles both "every 3 hours" and "every three hours"
	if strings.HasSuffix(lower, " hours") && strings.HasPrefix(lower, "every ") {
		rest := strings.TrimSpace(text[len("every "):])
		rest = strings.TrimSuffix(rest, " hours")
		rest = strings.TrimSpace(rest)

		n := parseNumberWord(rest)
		if n > 0 {
			return Schedule{Type: "interval", Interval: time.Duration(n) * time.Hour}, nil
		}
	}

	return Schedule{}, fmt.Errorf("unrecognized schedule format: %q", text)
}

// parseTime parses "HH" or "HH:MM" (24-hour) into hour and minute.
func parseTime(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, fmt.Errorf("empty time")
	}

	parts := strings.SplitN(s, ":", 2)
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse hour: %w", err)
	}
	if h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("hour out of range: %d", h)
	}

	m := 0
	if len(parts) > 1 {
		m, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("parse minute: %w", err)
		}
		if m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("minute out of range: %d", m)
		}
	}

	return h, m, nil
}

// parseNumberWord parses "3", "three", "6", "six", "12", "twelve", "24", "twentyfour" etc.
func parseNumberWord(s string) int {
	// try direct integer first
	n, err := strconv.Atoi(s)
	if err == nil && n > 0 {
		return n
	}

	wordMap := map[string]int{
		"one":   1,
		"two":   2,
		"three": 3,
		"four":  4,
		"five":  5,
		"six":   6,
		"seven": 7,
		"eight": 8,
		"nine":  9,
		"ten":   10,
		"eleven": 11,
		"twelve": 12,
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if n, ok := wordMap[s]; ok {
		return n
	}

	return 0
}
