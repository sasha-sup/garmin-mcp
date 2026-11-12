// Package civil provides a calendar date without time or time zone.
//
// Garmin keys almost all wellness data by the user's local calendar date
// ("2026-11-11"), so the server works with dates, not instants.
package civil

import (
	"fmt"
	"time"
)

// layout is the wire format used by Garmin and by the MCP tools.
const layout = "2006-01-02"

// Date is a calendar date. The zero value is not a valid date.
type Date struct {
	t time.Time // always midnight UTC
}

// Parse parses a YYYY-MM-DD string.
func Parse(s string) (Date, error) {
	t, err := time.Parse(layout, s)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q: want YYYY-MM-DD", s)
	}
	return Date{t: t}, nil
}

// Of returns the calendar date of t in t's location.
func Of(t time.Time) Date {
	y, m, d := t.Date()
	return Date{t: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// Today returns the current date in loc.
func Today(now time.Time, loc *time.Location) Date {
	return Of(now.In(loc))
}

// String formats d as YYYY-MM-DD.
func (d Date) String() string { return d.t.Format(layout) }

// AddDays returns d shifted by n days.
func (d Date) AddDays(n int) Date { return Date{t: d.t.AddDate(0, 0, n)} }

// Equal reports whether d and o are the same date.
func (d Date) Equal(o Date) bool { return d.t.Equal(o.t) }

// Before reports whether d is before o.
func (d Date) Before(o Date) bool { return d.t.Before(o.t) }

// After reports whether d is after o.
func (d Date) After(o Date) bool { return d.t.After(o.t) }

// DaysUntil returns the number of days from d to o (negative if o is earlier).
func (d Date) DaysUntil(o Date) int { return int(o.t.Sub(d.t).Hours() / 24) }

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.t.Weekday() }

// Month returns the first day of d's month.
func (d Date) Month() Date {
	return Date{t: time.Date(d.t.Year(), d.t.Month(), 1, 0, 0, 0, 0, time.UTC)}
}

// End returns the instant at which day d ends in loc.
func (d Date) End(loc *time.Location) time.Time {
	y, m, dd := d.t.Date()
	return time.Date(y, m, dd+1, 0, 0, 0, 0, loc)
}

// Range returns every date from start to end inclusive. It returns nil if
// end is before start.
func Range(start, end Date) []Date {
	if end.Before(start) {
		return nil
	}
	out := make([]Date, 0, start.DaysUntil(end)+1)
	for d := start; !d.After(end); d = d.AddDays(1) {
		out = append(out, d)
	}
	return out
}
