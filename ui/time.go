// Package ui formats values for display in user interfaces.
package ui

import (
	"strconv"
	"time"
)

const (
	defaultDateAfter  = 7 * 24 * time.Hour
	defaultDateLayout = "2 Jan 2006"
)

// RelativeTime formats past times relative to now, the way a person reads
// them: "just now", "5m ago", "3h ago", "2d ago", and then a date once the
// time is DateAfter or more in the past. The zero value is ready to use.
type RelativeTime struct {
	// DateAfter is how far in the past a time must be to show as a date
	// instead of a relative time. Zero means seven days.
	DateAfter time.Duration
	// DateLayout is the time.Format layout for dates. Empty means "2 Jan 2006".
	DateLayout string
	// Location is the time zone dates are shown in. Nil means time.Local.
	Location *time.Location
	// Zero is the text for the zero time, such as "never".
	Zero string
}

// Format formats t relative to the current time.
func (r RelativeTime) Format(t time.Time) string {
	return r.FormatAt(t, time.Now())
}

// FormatAt formats t relative to now. Times after now, such as from a
// slightly fast clock elsewhere, show as "just now".
func (r RelativeTime) FormatAt(t, now time.Time) string {
	if t.IsZero() {
		return r.Zero
	}

	d := max(now.Sub(t), 0)
	switch {
	case d >= r.dateAfter():
		return t.In(r.location()).Format(r.dateLayout())
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
}

func (r RelativeTime) dateAfter() time.Duration {
	if r.DateAfter == 0 {
		return defaultDateAfter
	}
	return r.DateAfter
}

func (r RelativeTime) dateLayout() string {
	if r.DateLayout == "" {
		return defaultDateLayout
	}
	return r.DateLayout
}

func (r RelativeTime) location() *time.Location {
	if r.Location == nil {
		return time.Local
	}
	return r.Location
}
