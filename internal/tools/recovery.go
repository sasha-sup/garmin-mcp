package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// DayInput selects one day.
type DayInput struct {
	Date string `json:"date,omitempty" jsonschema:"Day, YYYY-MM-DD. Default: today."`
}

// RecoveryOut is a one-day readiness snapshot.
type RecoveryOut struct {
	Date            string        `json:"date"`
	BaselineWindow  string        `json:"baseline_window"`
	Sleep           *SleepOut     `json:"last_night_sleep,omitempty"`
	HRV             *RecoveryHRV  `json:"hrv,omitempty"`
	RestingHR       Metric        `json:"resting_hr_bpm"`
	SleepHR         Metric        `json:"sleep_hr_bpm"`
	Respiration     Metric        `json:"sleep_respiration_brpm"`
	StressYesterday Metric        `json:"stress_avg_yesterday"`
	BodyBattery     *RecoveryBB   `json:"body_battery,omitempty"`
	Load            *RecoveryLoad `json:"training_load,omitempty"`
	Signals         []string      `json:"signals"`
	Method          string        `json:"method"`
	Missing         []string      `json:"missing_data,omitempty"`
}

// RecoveryHRV compares last night's HRV with Garmin's band and the personal mean.
type RecoveryHRV struct {
	LastNight    *int   `json:"last_night_ms,omitempty"`
	WeeklyAvg    *int   `json:"weekly_avg_ms,omitempty"`
	BaselineLow  *int   `json:"garmin_band_low_ms,omitempty"`
	BaselineHigh *int   `json:"garmin_band_high_ms,omitempty"`
	Status       string `json:"garmin_status,omitempty"`
	Personal     Metric `json:"vs_personal_baseline"`
}

// RecoveryBB summarises overnight recharge.
type RecoveryBB struct {
	SleepCharge *int `json:"charged_during_sleep,omitempty"`
	Max         *int `json:"max_today,omitempty"`
	Latest      *int `json:"latest,omitempty"`
}

// RecoveryLoad is the acute:chronic training load ratio computed here.
type RecoveryLoad struct {
	Acute7d       float64  `json:"last_7d"`
	ChronicWeekly float64  `json:"weekly_avg_last_28d"`
	ActiveWeeks   int      `json:"weeks_with_training_of_4"`
	Ratio         *float64 `json:"ratio_7d_to_28d,omitempty"`
	Note          string   `json:"note"`
}

const recoveryMethod = "Personal baseline = the 28 days before this day (baseline_days says how many had data; z-scores and baseline-based signals need >= 14). " +
	"Signals: sleep < 6 h or score < 60 (Garmin 'poor'); HRV below Garmin's own band or Garmin status LOW/UNBALANCED/POOR; resting HR z >= 1.5 or >= 5 bpm above mean; " +
	"yesterday's stress z >= 1; overnight Body Battery charge < 30; 2+ strain markers at once (HRV below band, resting HR z >= 1, sleep HR z >= 1, respiration >= 1 brpm above mean). " +
	"Training load uses completed days only (last 7 vs 28 days). These are heuristics computed by this server, not Garmin's Training Readiness, and not a diagnosis."

// statusFlags are the Garmin HRV statuses worth flagging. Others (BALANCED,
// empty or calibration states) are not warnings.
var statusFlags = map[string]bool{"LOW": true, "UNBALANCED": true, "POOR": true}

func (h *handlers) recoveryStatus(ctx context.Context, _ *mcp.CallToolRequest, in DayInput) (*mcp.CallToolResult, RecoveryOut, error) {
	day, err := h.parseDay(in.Date)
	if err != nil {
		return nil, RecoveryOut{}, err
	}
	if day.After(h.svc.Today()) {
		return nil, RecoveryOut{}, fmt.Errorf("date %s is in the future", day)
	}
	from, prior := day.AddDays(-baselineDays), day.AddDays(-1)

	var (
		wg                            sync.WaitGroup
		sleep                         map[string]garmin.DailySleep
		hrv                           map[string]garmin.HRVSummary
		sums                          map[string]garmin.DailySummary
		bb                            map[string]garmin.BodyBatteryReport
		acts                          []garmin.Activity
		eSleep, eHRV, eSum, eBB, eAct error
	)
	wg.Go(func() { sleep, eSleep = h.svc.Sleep(ctx, from, day) })
	wg.Go(func() { hrv, eHRV = h.svc.HRV(ctx, from, day) })
	wg.Go(func() { sums, eSum = h.svc.DailySummaries(ctx, from, day) })
	wg.Go(func() { bb, eBB = h.svc.BodyBattery(ctx, day, day) })
	wg.Go(func() { acts, eAct = h.svc.Activities(ctx, from, prior, "", "", 500) })
	wg.Wait()
	for _, e := range []error{eSleep, eHRV, eSum, eBB, eAct} {
		if e != nil {
			return nil, RecoveryOut{}, e
		}
	}

	out := RecoveryOut{
		Date:           day.String(),
		BaselineWindow: from.String() + ".." + prior.String(),
		Signals:        []string{},
		Method:         recoveryMethod,
	}
	key := day.String()
	signal := func(format string, a ...any) { out.Signals = append(out.Signals, fmt.Sprintf(format, a...)) }
	missing := func(s string) { out.Missing = append(out.Missing, s) }

	// history collects a metric over the baseline window (excluding day).
	history := func(get func(d string) *float64) []float64 {
		var v []float64
		for _, d := range civil.Range(from, prior) {
			if x := get(d.String()); x != nil {
				v = append(v, *x)
			}
		}
		return v
	}
	rhrOf := func(d string) *float64 {
		if s, ok := sums[d]; ok {
			return intToF(s.HeartRate.RestingValue)
		}
		return nil
	}
	stressOf := func(d string) *float64 {
		if s, ok := sums[d]; ok {
			return intToF(s.Stress.AvgLevel)
		}
		return nil
	}
	sleepHROf := func(d string) *float64 {
		if s, ok := sleep[d]; ok {
			return s.AvgHeartRate
		}
		return nil
	}
	respOf := func(d string) *float64 {
		if s, ok := sleep[d]; ok {
			return s.AverageRespirationValue
		}
		return nil
	}
	hrvOf := func(d string) *float64 {
		if v, ok := hrv[d]; ok {
			return intToF(v.LastNightAvg)
		}
		return nil
	}

	// Sleep.
	if s, ok := sleep[key]; ok && s.SleepTimeSeconds != nil && *s.SleepTimeSeconds > 0 {
		so := sleepOut(s)
		out.Sleep = &so
		if *so.SleepHours < 6 {
			signal("short sleep: %.1f h", *so.SleepHours)
		}
		if so.Score != nil && *so.Score < 60 {
			signal("poor sleep score: %.0f", *so.Score)
		}
	} else {
		missing("last night's sleep (not recorded or not synced yet)")
	}
	out.SleepHR = newMetric(sleepHROf(key), history(sleepHROf), 0)
	out.Respiration = newMetric(respOf(key), history(respOf), 1)

	// HRV.
	hrvBelowBand := false
	if v, ok := hrv[key]; ok && v.LastNightAvg != nil {
		r := &RecoveryHRV{LastNight: v.LastNightAvg, WeeklyAvg: v.WeeklyAvg, Status: v.Status,
			Personal: newMetric(hrvOf(key), history(hrvOf), 0)}
		if v.Baseline != nil {
			r.BaselineLow, r.BaselineHigh = v.Baseline.BalancedLow, v.Baseline.BalancedUpper
		}
		out.HRV = r
		if r.BaselineLow != nil && *r.LastNight < *r.BaselineLow {
			hrvBelowBand = true
			signal("HRV below your Garmin baseline band: %d ms vs %d-%d ms", *r.LastNight, *r.BaselineLow, deref(r.BaselineHigh))
		}
		if statusFlags[v.Status] {
			signal("Garmin HRV status: %s", v.Status)
		}
	} else {
		missing("last night's HRV")
	}

	// Resting heart rate.
	out.RestingHR = newMetric(rhrOf(key), history(rhrOf), 0)
	if out.RestingHR.Value == nil {
		missing("today's resting HR (usually appears after the watch syncs)")
	} else if d, ok := out.RestingHR.delta(); ok && (d >= 5 || out.RestingHR.zAtLeast(1.5)) {
		signal("resting HR above your usual: +%.0f bpm vs your 28-day mean", d)
	}

	// Stress yesterday.
	out.StressYesterday = newMetric(stressOf(prior.String()), history(stressOf), 0)
	if out.StressYesterday.zAtLeast(1) {
		signal("yesterday's stress above your usual: average %.0f vs your mean %.0f", *out.StressYesterday.Value, *out.StressYesterday.Mean)
	}

	// Several strain markers at once.
	var markers []string
	if hrvBelowBand {
		markers = append(markers, "HRV below band")
	}
	if out.RestingHR.zAtLeast(1) {
		markers = append(markers, "resting HR up")
	}
	if out.SleepHR.zAtLeast(1) {
		markers = append(markers, "sleep HR up")
	}
	if d, ok := out.Respiration.delta(); ok && d >= 1 {
		markers = append(markers, "breathing rate up")
	}
	if len(markers) >= 2 {
		signal("%d strain markers together (%s): a pattern seen with illness onset, hard training, alcohol, heat or poor recovery; an easy day is reasonable",
			len(markers), strings.Join(markers, ", "))
	}

	// Body Battery. The SLEEP event of the night before day D is in day D's report.
	if r, ok := bb[key]; ok {
		o := bbOut(r, h.svc.Location(), false)
		b := &RecoveryBB{Max: o.Max, Latest: o.Latest}
		for _, e := range o.Events {
			if e.Type == "SLEEP" && e.Impact != nil {
				b.SleepCharge = e.Impact
			}
		}
		out.BodyBattery = b
		if b.SleepCharge != nil && *b.SleepCharge < 30 {
			signal("weak overnight recharge: Body Battery +%d", *b.SleepCharge)
		}
	} else {
		missing("today's Body Battery")
	}

	// Training load over completed days.
	var acute, total float64
	weeks := [4]float64{}
	for _, a := range acts {
		if a.ActivityTrainingLoad == nil {
			continue
		}
		d, err := civil.Parse(firstN(a.StartTimeLocal, 10))
		if err != nil || d.Before(from) || !d.Before(day) {
			continue
		}
		total += *a.ActivityTrainingLoad
		w := d.DaysUntil(prior) / 7 // 0 = last 7 days
		if w >= 0 && w < 4 {
			weeks[w] += *a.ActivityTrainingLoad
		}
		if w == 0 {
			acute += *a.ActivityTrainingLoad
		}
	}
	if total > 0 {
		l := &RecoveryLoad{Acute7d: round(acute, 0), ChronicWeekly: round(total/4, 0),
			Note: "Sum of Garmin per-activity training load; an approximation, Garmin's Acute Load is not in this API."}
		for _, w := range weeks {
			if w > 0 {
				l.ActiveWeeks++
			}
		}
		if l.ActiveWeeks >= 3 {
			r := round(acute/(total/4), 2)
			l.Ratio = &r
			switch {
			case r > 1.5:
				signal("training load sharply above your usual: last 7 days = %.2f x your 4-week weekly average", r)
			case r < 0.8:
				signal("training load lower than usual (%.2f x): a rest or taper week, or reduced training", r)
			}
		} else {
			l.Note += " Ratio omitted: training in fewer than 3 of the last 4 weeks."
		}
		out.Load = l
	}

	if out.RestingHR.N < minBaselineDays {
		missing(fmt.Sprintf("personal baseline: only %d of 28 days have resting HR, so comparisons with your usual are skipped", out.RestingHR.N))
	}
	if len(out.Signals) == 0 {
		if len(out.Missing) == 0 {
			signal("no warning signals: metrics are within your usual range")
		} else {
			signal("no warning signals in the available data (see missing_data)")
		}
	}
	return nil, out, nil
}
