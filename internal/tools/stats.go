package tools

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// Personal baselines: the 28 days before the analysed day; fewer than
// minBaselineDays days of history is too little to call anything unusual.
const (
	baselineDays    = 28
	minBaselineDays = 14
)

// Metric is a value compared with the user's own recent history.
type Metric struct {
	Value *float64 `json:"value,omitempty"`
	Mean  *float64 `json:"personal_mean,omitempty"`
	SD    *float64 `json:"personal_sd,omitempty"`
	N     int      `json:"baseline_days"`
	Z     *float64 `json:"z_score,omitempty"`
}

// newMetric builds a Metric. The z-score is only set with enough history.
func newMetric(value *float64, history []float64, places int) Metric {
	m := Metric{Value: rp(value, places), N: len(history)}
	if len(history) == 0 {
		return m
	}
	mu := meanOf(history)
	m.Mean = new(round(mu, places))
	if len(history) >= 2 {
		m.SD = new(round(stddev(history), places+1))
	}
	if value != nil && len(history) >= minBaselineDays && m.SD != nil && *m.SD > 0 {
		m.Z = new(round((*value-mu)/stddev(history), 1))
	}
	return m
}

func (m Metric) delta() (float64, bool) {
	if m.Value == nil || m.Mean == nil || m.N < minBaselineDays {
		return 0, false
	}
	return *m.Value - *m.Mean, true
}

func (m Metric) zAtLeast(z float64) bool { return m.Z != nil && *m.Z >= z }

func meanOf(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// percentile returns the p-th percentile (0-100) by nearest rank.
func percentile(v []float64, p float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// stddev is the sample standard deviation.
func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m := meanOf(v)
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(v)-1))
}

// slope is the least-squares slope of ys over xs.
func slope(xs, ys []float64) float64 {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

// sleepTiming returns sleep onset as minutes since 18:00 local and wake time
// as onset plus time in bed, so both stay continuous across midnight. Garmin's
// *Local timestamps are epoch milliseconds already shifted to local time.
func sleepTiming(s garmin.DailySleep) (onset, wake float64, ok bool) {
	if s.SleepStartTimestampLocal == nil || s.SleepEndTimestampLocal == nil || *s.SleepStartTimestampLocal == 0 {
		return 0, 0, false
	}
	start := time.UnixMilli(*s.SleepStartTimestampLocal).UTC()
	inBed := float64(*s.SleepEndTimestampLocal-*s.SleepStartTimestampLocal) / 60000
	if inBed <= 0 || inBed > 24*60 {
		return 0, 0, false
	}
	m := float64(start.Hour()*60+start.Minute()) - 18*60
	if m < 0 {
		m += 24 * 60
	}
	return m, m + inBed, true
}

func clockFrom18(m float64) string {
	total := ((int(math.Round(m))+18*60)%(24*60) + 24*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
