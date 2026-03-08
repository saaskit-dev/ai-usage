package provider

import (
	"fmt"
	"time"
)

// FormatDuration formats the time remaining until t as a human-readable string.
func FormatDuration(t time.Time) string {
	now := time.Now()
	seconds := t.Unix() - now.Unix()
	if seconds <= 0 {
		return "Resets soon"
	}

	days := int(seconds / 86400)
	hours := int((seconds % 86400) / 3600)
	minutes := int((seconds % 3600) / 60)

	if days > 0 {
		return fmt.Sprintf("Resets in %dd %dh", days, hours)
	} else if hours > 0 {
		return fmt.Sprintf("Resets in %dh %dm", hours, minutes)
	} else if minutes > 0 {
		return fmt.Sprintf("Resets in %dm", minutes)
	}
	return "Resets soon"
}

// ParseTime tries multiple common time layouts and returns the parsed time.
// Returns zero time if none match.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02",
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t
		}
	}
	return time.Time{}
}

// FormatResetText parses a date string and returns a human-readable reset text.
func FormatResetText(dateStr string) string {
	t := ParseTime(dateStr)
	if t.IsZero() {
		return ""
	}
	return FormatDuration(t)
}
