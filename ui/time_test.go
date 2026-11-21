package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRelativeTimeFormatAt(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"same instant", 0, "just now"},
		{"in the future", -5 * time.Minute, "just now"},
		{"under a minute", time.Minute - time.Nanosecond, "just now"},
		{"a minute", time.Minute, "1m ago"},
		{"under an hour", time.Hour - time.Nanosecond, "59m ago"},
		{"an hour", time.Hour, "1h ago"},
		{"under a day", day - time.Nanosecond, "23h ago"},
		{"a day", day, "1d ago"},
		{"under the cut-over", 7*day - time.Nanosecond, "6d ago"},
		{"at the cut-over", 7 * day, "8 Mar 2026"},
		{"past the cut-over", 400 * day, "8 Feb 2025"},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				r := RelativeTime{Location: time.UTC}
				assert.Equal(t, tt.want, r.FormatAt(now.Add(-tt.ago), now))
			},
		)
	}
}

func TestRelativeTimeZero(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	assert.Empty(t, RelativeTime{}.FormatAt(time.Time{}, now))
	assert.Equal(t, "never", RelativeTime{Zero: "never"}.FormatAt(time.Time{}, now))
}

func TestRelativeTimeDateAfter(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	r := RelativeTime{DateAfter: 30 * 24 * time.Hour, Location: time.UTC}

	assert.Equal(t, "29d ago", r.FormatAt(now.Add(-30*24*time.Hour+time.Nanosecond), now))
	assert.Equal(t, "13 Feb 2026", r.FormatAt(now.Add(-30*24*time.Hour), now))
}

func TestRelativeTimeDateAfterBelowADay(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	r := RelativeTime{DateAfter: 2 * time.Hour, Location: time.UTC}

	assert.Equal(t, "1h ago", r.FormatAt(now.Add(-time.Hour), now))
	assert.Equal(t, "15 Mar 2026", r.FormatAt(now.Add(-2*time.Hour), now))
}

func TestRelativeTimeDateLayout(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	r := RelativeTime{DateLayout: "2006-01-02", Location: time.UTC}

	assert.Equal(t, "2026-03-01", r.FormatAt(now.Add(-14*24*time.Hour), now))
}

func TestRelativeTimeLocation(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	then := time.Date(2026, 3, 1, 23, 0, 0, 0, time.UTC)
	ahead := time.FixedZone("ahead", 2*60*60)

	assert.Equal(t, "2 Mar 2026", RelativeTime{Location: ahead}.FormatAt(then, now))
}

func TestRelativeTimeDefaultLocation(t *testing.T) {
	local := time.Local
	t.Cleanup(func() { time.Local = local })
	time.Local = time.FixedZone("behind", -2*60*60)

	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	then := time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)

	assert.Equal(t, "28 Feb 2026", RelativeTime{}.FormatAt(then, now))
}

func TestRelativeTimeFormat(t *testing.T) {
	assert.Equal(t, "just now", RelativeTime{}.Format(time.Now()))
	assert.Equal(t, "5m ago", RelativeTime{}.Format(time.Now().Add(-5*time.Minute-time.Second)))
}
