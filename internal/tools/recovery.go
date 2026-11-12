package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	d, err := h.loadRecovery(ctx, day)
	if err != nil {
		return nil, RecoveryOut{}, err
	}

	out := RecoveryOut{
		Date:           day.String(),
		BaselineWindow: d.from.String() + ".." + d.prior.String(),
		Signals:        []string{},
		Method:         recoveryMethod,
	}
	d.addSleep(&out)
	hrvBelowBand := d.addHRV(&out)
	d.addRestingHRAndStress(&out)
	addStrainMarkers(&out, hrvBelowBand)
	d.addBodyBattery(&out, h.svc.Location())
	if out.Load = trainingLoad(d.acts, d.from, day); out.Load != nil {
		out.signal("%s", loadSignal(out.Load))
	}
	finishRecovery(&out)
	return nil, out, nil
}

// recoveryData is everything one snapshot needs: the day plus its 28-day baseline window.
type recoveryData struct {
	day, from, prior civil.Date
	sleep            map[string]garmin.DailySleep
	hrv              map[string]garmin.HRVSummary
	sums             map[string]garmin.DailySummary
	bb               map[string]garmin.BodyBatteryReport
	acts             []garmin.Activity
}

func (h *handlers) loadRecovery(ctx context.Context, day civil.Date) (*recoveryData, error) {
	d := &recoveryData{day: day, from: day.AddDays(-baselineDays), prior: day.AddDays(-1)}
	err := fetchAll(
		func() (err error) { d.sleep, err = h.svc.Sleep(ctx, d.from, day); return },
		func() (err error) { d.hrv, err = h.svc.HRV(ctx, d.from, day); return },
		func() (err error) { d.sums, err = h.svc.DailySummaries(ctx, d.from, day); return },
		func() (err error) { d.bb, err = h.svc.BodyBattery(ctx, day, day); return },
		func() (err error) {
			d.acts, err = h.svc.Activities(ctx, d.from, d.prior, "", "", maxActivitiesPerAnalysis)
			return
		},
	)
	return d, err
}

// Per-day accessors. Missing days are zero structs, so they yield nil.
func (d *recoveryData) rhr(day string) *float64     { return intToF(d.sums[day].HeartRate.RestingValue) }
func (d *recoveryData) stress(day string) *float64  { return intToF(d.sums[day].Stress.AvgLevel) }
func (d *recoveryData) sleepHR(day string) *float64 { return d.sleep[day].AvgHeartRate }
func (d *recoveryData) resp(day string) *float64    { return d.sleep[day].AverageRespirationValue }
func (d *recoveryData) hrvMs(day string) *float64   { return intToF(d.hrv[day].LastNightAvg) }

// metric compares get(at) with get over the baseline window.
func (d *recoveryData) metric(at civil.Date, get func(string) *float64, places int) Metric {
	var history []float64
	for _, day := range civil.Range(d.from, d.prior) {
		if x := get(day.String()); x != nil {
			history = append(history, *x)
		}
	}
	return newMetric(get(at.String()), history, places)
}

// signal adds a formatted signal; an empty result is ignored.
func (o *RecoveryOut) signal(format string, a ...any) {
	if msg := fmt.Sprintf(format, a...); msg != "" {
		o.Signals = append(o.Signals, msg)
	}
}

func (o *RecoveryOut) missing(what string) { o.Missing = append(o.Missing, what) }

func (d *recoveryData) addSleep(out *RecoveryOut) {
	if s := d.sleep[d.day.String()]; s.SleepTimeSeconds != nil && *s.SleepTimeSeconds > 0 {
		so := sleepOut(s)
		out.Sleep = &so
		if *so.SleepHours < 6 {
			out.signal("short sleep: %.1f h", *so.SleepHours)
		}
		if so.Score != nil && *so.Score < 60 {
			out.signal("poor sleep score: %.0f", *so.Score)
		}
	} else {
		out.missing("last night's sleep (not recorded or not synced yet)")
	}
	out.SleepHR = d.metric(d.day, d.sleepHR, 0)
	out.Respiration = d.metric(d.day, d.resp, 1)
}

// addHRV reports whether last night's HRV is below Garmin's baseline band.
func (d *recoveryData) addHRV(out *RecoveryOut) bool {
	v := d.hrv[d.day.String()]
	if v.LastNightAvg == nil {
		out.missing("last night's HRV")
		return false
	}
	r := &RecoveryHRV{LastNight: v.LastNightAvg, WeeklyAvg: v.WeeklyAvg, Status: v.Status,
		Personal: d.metric(d.day, d.hrvMs, 0)}
	if v.Baseline != nil {
		r.BaselineLow, r.BaselineHigh = v.Baseline.BalancedLow, v.Baseline.BalancedUpper
	}
	out.HRV = r
	below := r.BaselineLow != nil && *r.LastNight < *r.BaselineLow
	if below {
		out.signal("HRV below your Garmin baseline band: %d ms vs %d-%d ms", *r.LastNight, *r.BaselineLow, deref(r.BaselineHigh))
	}
	if statusFlags[v.Status] {
		out.signal("Garmin HRV status: %s", v.Status)
	}
	return below
}

func (d *recoveryData) addRestingHRAndStress(out *RecoveryOut) {
	out.RestingHR = d.metric(d.day, d.rhr, 0)
	if out.RestingHR.Value == nil {
		out.missing("today's resting HR (usually appears after the watch syncs)")
	} else if delta, ok := out.RestingHR.delta(); ok && (delta >= 5 || out.RestingHR.zAtLeast(1.5)) {
		out.signal("resting HR above your usual: +%.0f bpm vs your 28-day mean", delta)
	}
	out.StressYesterday = d.metric(d.prior, d.stress, 0)
	if out.StressYesterday.zAtLeast(1) {
		out.signal("yesterday's stress above your usual: average %.0f vs your mean %.0f", *out.StressYesterday.Value, *out.StressYesterday.Mean)
	}
}

func addStrainMarkers(out *RecoveryOut, hrvBelowBand bool) {
	respUp := false
	if delta, ok := out.Respiration.delta(); ok && delta >= 1 {
		respUp = true
	}
	var markers []string
	for _, m := range []struct {
		on   bool
		name string
	}{
		{hrvBelowBand, "HRV below band"},
		{out.RestingHR.zAtLeast(1), "resting HR up"},
		{out.SleepHR.zAtLeast(1), "sleep HR up"},
		{respUp, "breathing rate up"},
	} {
		if m.on {
			markers = append(markers, m.name)
		}
	}
	if len(markers) >= 2 {
		out.signal("%d strain markers together (%s): a pattern seen with illness onset, hard training, alcohol, heat or poor recovery; an easy day is reasonable",
			len(markers), strings.Join(markers, ", "))
	}
}

// addBodyBattery uses day D's report, which holds the SLEEP event of the night before D.
func (d *recoveryData) addBodyBattery(out *RecoveryOut, loc *time.Location) {
	r, ok := d.bb[d.day.String()]
	if !ok {
		out.missing("today's Body Battery")
		return
	}
	o := bbOut(r, loc, false)
	b := &RecoveryBB{Max: o.Max, Latest: o.Latest}
	for _, e := range o.Events {
		if e.Type == "SLEEP" && e.Impact != nil {
			b.SleepCharge = e.Impact
		}
	}
	out.BodyBattery = b
	if b.SleepCharge != nil && *b.SleepCharge < 30 {
		out.signal("weak overnight recharge: Body Battery +%d", *b.SleepCharge)
	}
}

// trainingLoad sums Garmin per-activity load per week over the 28 completed
// days before day. It returns nil when there was no load at all.
func trainingLoad(acts []garmin.Activity, from, day civil.Date) *RecoveryLoad {
	var weeks [4]float64 // 0 = the last 7 days
	prior := day.AddDays(-1)
	for _, a := range acts {
		d, err := civil.Parse(firstN(a.StartTimeLocal, 10))
		if a.ActivityTrainingLoad == nil || err != nil || d.Before(from) || !d.Before(day) {
			continue
		}
		weeks[d.DaysUntil(prior)/7] += *a.ActivityTrainingLoad
	}
	total := weeks[0] + weeks[1] + weeks[2] + weeks[3]
	if total == 0 {
		return nil
	}
	l := &RecoveryLoad{Acute7d: round(weeks[0], 0), ChronicWeekly: round(total/4, 0),
		Note: "Sum of Garmin per-activity training load; an approximation, Garmin's Acute Load is not in this API."}
	for _, w := range weeks {
		if w > 0 {
			l.ActiveWeeks++
		}
	}
	if l.ActiveWeeks < 3 {
		l.Note += " Ratio omitted: training in fewer than 3 of the last 4 weeks."
		return l
	}
	l.Ratio = new(round(weeks[0]/(total/4), 2))
	return l
}

func loadSignal(l *RecoveryLoad) string {
	switch {
	case l.Ratio == nil:
		return ""
	case *l.Ratio > 1.5:
		return fmt.Sprintf("training load sharply above your usual: last 7 days = %.2f x your 4-week weekly average", *l.Ratio)
	case *l.Ratio < 0.8:
		return fmt.Sprintf("training load lower than usual (%.2f x): a rest or taper week, or reduced training", *l.Ratio)
	}
	return ""
}

func finishRecovery(out *RecoveryOut) {
	if out.RestingHR.N < minBaselineDays {
		out.missing(fmt.Sprintf("personal baseline: only %d of 28 days have resting HR, so comparisons with your usual are skipped", out.RestingHR.N))
	}
	switch {
	case len(out.Signals) > 0:
	case len(out.Missing) == 0:
		out.signal("no warning signals: metrics are within your usual range")
	default:
		out.signal("no warning signals in the available data (see missing_data)")
	}
}
