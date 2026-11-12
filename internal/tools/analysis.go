package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// --- garmin_recovery_status ---

// DayInput selects one day.
type DayInput struct {
	Date string `json:"date,omitempty" jsonschema:"Day, YYYY-MM-DD. Default: today."`
}

// RecoveryOut is a one-day readiness snapshot.
type RecoveryOut struct {
	Date        string        `json:"date"`
	Sleep       *SleepOut     `json:"last_night_sleep,omitempty"`
	HRV         *RecoveryHRV  `json:"hrv,omitempty"`
	RestingHR   *RecoveryRHR  `json:"resting_hr,omitempty"`
	BodyBattery *RecoveryBB   `json:"body_battery,omitempty"`
	StressYday  *int          `json:"stress_avg_yesterday,omitempty"`
	Load        *RecoveryLoad `json:"training_load,omitempty"`
	Signals     []string      `json:"signals"`
	Thresholds  string        `json:"thresholds"`
	Missing     []string      `json:"missing_data,omitempty"`
}

// RecoveryHRV compares last night's HRV with the baseline.
type RecoveryHRV struct {
	LastNight    *int     `json:"last_night_ms,omitempty"`
	WeeklyAvg    *int     `json:"weekly_avg_ms,omitempty"`
	BaselineLow  *int     `json:"baseline_low_ms,omitempty"`
	BaselineHigh *int     `json:"baseline_high_ms,omitempty"`
	Mean28d      *float64 `json:"mean_28d_ms,omitempty"`
	Status       string   `json:"garmin_status,omitempty"`
}

// RecoveryRHR compares today's resting HR with recent history.
type RecoveryRHR struct {
	Today   *int     `json:"today_bpm,omitempty"`
	Mean28d *float64 `json:"mean_28d_bpm,omitempty"`
	Delta   *float64 `json:"delta_vs_28d_bpm,omitempty"`
}

// RecoveryBB summarises overnight recharge.
type RecoveryBB struct {
	SleepCharge *int `json:"charged_during_sleep,omitempty"`
	Max         *int `json:"max_today,omitempty"`
	Latest      *int `json:"latest,omitempty"`
}

// RecoveryLoad is the acute:chronic training load ratio computed here.
type RecoveryLoad struct {
	Acute7d     float64  `json:"acute_7d"`
	ChronicWeek float64  `json:"chronic_weekly_avg_28d"`
	Ratio       *float64 `json:"acute_chronic_ratio,omitempty"`
	Note        string   `json:"note"`
}

const recoveryThresholds = "Computed by this server, not by Garmin: HRV below baseline_low = suppressed; resting HR >= 5 bpm above 28-day mean = elevated; " +
	"sleep < 6 h or score < 60 = poor; overnight Body Battery charge < 30 = weak recharge; load ratio > 1.5 = sharp increase, < 0.8 = detraining."

func (h *handlers) recoveryStatus(ctx context.Context, _ *mcp.CallToolRequest, in DayInput) (*mcp.CallToolResult, RecoveryOut, error) {
	day, err := h.parseDay(in.Date)
	if err != nil {
		return nil, RecoveryOut{}, err
	}
	from := day.AddDays(-27)

	var (
		wg                            sync.WaitGroup
		sleep                         map[string]garmin.DailySleep
		hrv                           map[string]garmin.HRVSummary
		sums                          map[string]garmin.DailySummary
		bb                            map[string]garmin.BodyBatteryReport
		acts                          []garmin.Activity
		eSleep, eHRV, eSum, eBB, eAct error
	)
	wg.Go(func() { sleep, eSleep = h.svc.Sleep(ctx, day, day) })
	wg.Go(func() { hrv, eHRV = h.svc.HRV(ctx, from, day) })
	wg.Go(func() { sums, eSum = h.svc.DailySummaries(ctx, from, day) })
	wg.Go(func() { bb, eBB = h.svc.BodyBattery(ctx, day, day) })
	wg.Go(func() { acts, eAct = h.svc.Activities(ctx, from, day, "", "", 500) })
	wg.Wait()
	for _, e := range []error{eSleep, eHRV, eSum, eBB, eAct} {
		if e != nil {
			return nil, RecoveryOut{}, e
		}
	}

	out := RecoveryOut{Date: day.String(), Signals: []string{}, Thresholds: recoveryThresholds}
	key := day.String()

	if s, ok := sleep[key]; ok && s.SleepTimeSeconds != nil {
		so := sleepOut(s)
		out.Sleep = &so
		if so.SleepHours != nil && *so.SleepHours < 6 {
			out.Signals = append(out.Signals, fmt.Sprintf("short sleep: %.1f h", *so.SleepHours))
		}
		if so.Score != nil && *so.Score < 60 {
			out.Signals = append(out.Signals, fmt.Sprintf("low sleep score: %.0f (%s)", *so.Score, so.ScoreQualifier))
		}
	} else {
		out.Missing = append(out.Missing, "sleep for last night")
	}

	if v, ok := hrv[key]; ok {
		r := &RecoveryHRV{LastNight: v.LastNightAvg, WeeklyAvg: v.WeeklyAvg, Status: v.Status}
		if v.Baseline != nil {
			r.BaselineLow, r.BaselineHigh = v.Baseline.BalancedLow, v.Baseline.BalancedUpper
		}
		var m mean
		for k, x := range hrv {
			if k != key {
				m.addInt(x.LastNightAvg)
			}
		}
		r.Mean28d = m.value(1)
		out.HRV = r
		if r.LastNight != nil && r.BaselineLow != nil && *r.LastNight < *r.BaselineLow {
			out.Signals = append(out.Signals, fmt.Sprintf("HRV suppressed: %d ms vs baseline %d-%d ms", *r.LastNight, *r.BaselineLow, deref(r.BaselineHigh)))
		}
		if v.Status != "" && v.Status != "BALANCED" {
			out.Signals = append(out.Signals, "Garmin HRV status: "+v.Status)
		}
	} else {
		out.Missing = append(out.Missing, "HRV for last night")
	}

	if s, ok := sums[key]; ok && s.HeartRate.RestingValue != nil {
		var m mean
		for k, x := range sums {
			if k != key {
				m.addInt(x.HeartRate.RestingValue)
			}
		}
		r := &RecoveryRHR{Today: s.HeartRate.RestingValue, Mean28d: m.value(1)}
		if r.Mean28d != nil {
			d := round(float64(*r.Today)-*r.Mean28d, 1)
			r.Delta = &d
			if d >= 5 {
				out.Signals = append(out.Signals, fmt.Sprintf("resting HR elevated: +%.1f bpm vs 28-day mean (possible fatigue, illness, alcohol, heat or stress)", d))
			}
		}
		out.RestingHR = r
	} else {
		out.Missing = append(out.Missing, "resting HR for today (often appears after the watch syncs)")
	}

	if y, ok := sums[day.AddDays(-1).String()]; ok {
		out.StressYday = y.Stress.AvgLevel
		if y.Stress.AvgLevel != nil && *y.Stress.AvgLevel >= 50 {
			out.Signals = append(out.Signals, fmt.Sprintf("high stress yesterday: average %d", *y.Stress.AvgLevel))
		}
	}

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
			out.Signals = append(out.Signals, fmt.Sprintf("weak overnight recharge: Body Battery +%d", *b.SleepCharge))
		}
	}

	acute7, total28 := 0.0, 0.0
	for _, a := range acts {
		if a.ActivityTrainingLoad == nil {
			continue
		}
		d, err := civil.Parse(firstN(a.StartTimeLocal, 10))
		if err != nil {
			continue
		}
		total28 += *a.ActivityTrainingLoad
		if !d.Before(day.AddDays(-6)) {
			acute7 += *a.ActivityTrainingLoad
		}
	}
	if total28 > 0 {
		l := &RecoveryLoad{Acute7d: round(acute7, 0), ChronicWeek: round(total28/4, 0),
			Note: "Sum of Garmin per-activity training load; an approximation of Garmin's Acute Load, which this API does not expose."}
		if l.ChronicWeek > 0 {
			r := round(acute7/(total28/4), 2)
			l.Ratio = &r
			switch {
			case r > 1.5:
				out.Signals = append(out.Signals, fmt.Sprintf("training load rising sharply: ratio %.2f", r))
			case r < 0.8:
				out.Signals = append(out.Signals, fmt.Sprintf("training load below usual: ratio %.2f", r))
			}
		}
		out.Load = l
	}
	if len(out.Signals) == 0 && len(out.Missing) == 0 {
		out.Signals = append(out.Signals, "no warning signals: metrics are within personal norms")
	}
	return nil, out, nil
}

// --- garmin_sleep_analysis ---

// SleepAnalysisOut summarises sleep quality and its likely drivers.
type SleepAnalysisOut struct {
	From       string                    `json:"from"`
	To         string                    `json:"to"`
	Nights     int                       `json:"nights_with_data"`
	Averages   SleepAverages             `json:"averages"`
	Regularity SleepRegularity           `json:"regularity"`
	StageShare SleepStageShare           `json:"stage_share_pct"`
	Qualifiers map[string]map[string]int `json:"sub_score_counts,omitempty"`
	ScoreTrend *float64                  `json:"score_trend_per_week,omitempty"`
	Factors    []SleepFactor             `json:"factors"`
	Worst      []SleepOut                `json:"worst_nights"`
	Best       []SleepOut                `json:"best_nights"`
	Notes      []string                  `json:"notes"`
}

// SleepAverages are means over the period.
type SleepAverages struct {
	SleepHours     *float64 `json:"sleep_hours,omitempty"`
	SleepNeedHours *float64 `json:"sleep_need_hours,omitempty"`
	DebtHoursWeek  *float64 `json:"sleep_debt_hours_per_week,omitempty"`
	Score          *float64 `json:"score,omitempty"`
	AwakeCount     *float64 `json:"awake_count,omitempty"`
	AvgHR          *float64 `json:"sleep_hr_bpm,omitempty"`
	RestingHR      *float64 `json:"resting_hr_bpm,omitempty"`
	HRV            *float64 `json:"hrv_ms,omitempty"`
	SleepStress    *float64 `json:"sleep_stress,omitempty"`
	SpO2           *float64 `json:"spo2_pct,omitempty"`
	LowestSpO2     *float64 `json:"lowest_spo2_pct,omitempty"`
	Respiration    *float64 `json:"respiration_brpm,omitempty"`
}

// SleepRegularity describes the timing of sleep.
type SleepRegularity struct {
	MedianBedtime    string   `json:"median_bedtime,omitempty"`
	MedianWake       string   `json:"median_wake_time,omitempty"`
	BedtimeSpreadMin *float64 `json:"bedtime_stddev_min,omitempty"`
	WakeSpreadMin    *float64 `json:"wake_stddev_min,omitempty"`
	SocialJetlagMin  *float64 `json:"social_jetlag_min,omitempty"`
	ShortNights      int      `json:"nights_under_6h"`
}

// SleepStageShare is the mean share of each stage in total sleep.
type SleepStageShare struct {
	Deep  *float64 `json:"deep,omitempty"`
	Light *float64 `json:"light,omitempty"`
	REM   *float64 `json:"rem,omitempty"`
}

// SleepFactor compares sleep score after days with and without a condition.
type SleepFactor struct {
	Factor       string   `json:"factor"`
	NightsWith   int      `json:"nights_with"`
	ScoreWith    *float64 `json:"avg_score_with,omitempty"`
	ScoreWithout *float64 `json:"avg_score_without,omitempty"`
	Difference   *float64 `json:"difference,omitempty"`
}

func (h *handlers) sleepAnalysis(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, SleepAnalysisOut, error) {
	from, to, err := h.resolve(in, 28, 366)
	if err != nil {
		return nil, SleepAnalysisOut{}, err
	}
	prev := from.AddDays(-1) // the day before each night
	var (
		wg                       sync.WaitGroup
		sleep                    map[string]garmin.DailySleep
		hrv                      map[string]garmin.HRVSummary
		sums                     map[string]garmin.DailySummary
		acts                     []garmin.Activity
		eSleep, eHRV, eSum, eAct error
	)
	wg.Go(func() { sleep, eSleep = h.svc.Sleep(ctx, from, to) })
	wg.Go(func() { hrv, eHRV = h.svc.HRV(ctx, from, to) })
	wg.Go(func() { sums, eSum = h.svc.DailySummaries(ctx, prev, to) })
	wg.Go(func() { acts, eAct = h.svc.Activities(ctx, prev, to, "", "", 1000) })
	wg.Wait()
	for _, e := range []error{eSleep, eHRV, eSum, eAct} {
		if e != nil {
			return nil, SleepAnalysisOut{}, e
		}
	}

	out := SleepAnalysisOut{From: from.String(), To: to.String(), Factors: []SleepFactor{}, Worst: []SleepOut{}, Best: []SleepOut{}, Notes: []string{}}

	// Latest workout end time (local clock) per day, and load per day.
	lateWorkout := map[string]bool{}
	hardDay := map[string]bool{}
	for _, a := range acts {
		date := firstN(a.StartTimeLocal, 10)
		if t, err := time.Parse("2006-01-02 15:04:05", firstN(a.StartTimeLocal, 19)); err == nil && a.Duration != nil {
			end := t.Add(time.Duration(*a.Duration) * time.Second)
			if end.Hour() >= 20 || civil.Of(end).String() != date {
				lateWorkout[date] = true
			}
		}
		if a.ActivityTrainingLoad != nil && *a.ActivityTrainingLoad >= 150 {
			hardDay[date] = true
		}
	}

	type night struct {
		date      civil.Date
		s         SleepOut
		bedMin    float64 // minutes from 18:00, so evening/after-midnight bedtimes are continuous
		wakeMin   float64
		hasTiming bool
	}
	var nights []night
	var avg struct{ hours, need, score, awake, hr, rhr, hrv, stress, spo2, low, resp, deep, light, rem mean }
	short := 0
	for _, k := range sortedKeys(sleep) {
		so := sleepOut(sleep[k])
		if so.SleepHours == nil {
			continue
		}
		d, _ := civil.Parse(k)
		n := night{date: d, s: so}
		if b, w, ok := sleepTiming(sleep[k]); ok {
			n.bedMin, n.wakeMin, n.hasTiming = b, w, true
		}
		nights = append(nights, n)
		avg.hours.add(so.SleepHours)
		if so.SleepNeedMin != nil {
			avg.need.add(new(float64(*so.SleepNeedMin) / 60))
		}
		avg.score.add(so.Score)
		avg.awake.addInt(so.AwakeCount)
		avg.hr.add(so.AvgHR)
		avg.stress.add(so.AvgStress)
		avg.spo2.add(so.AvgSpO2)
		avg.low.add(so.LowestSpO2)
		avg.resp.add(so.AvgRespiration)
		if s, ok := sums[k]; ok {
			avg.rhr.addInt(s.HeartRate.RestingValue)
		}
		if v, ok := hrv[k]; ok {
			avg.hrv.addInt(v.LastNightAvg)
		}
		total := float64(deref(so.DeepMin) + deref(so.LightMin) + deref(so.RemMin))
		if total > 0 {
			avg.deep.add(new(float64(deref(so.DeepMin)) / total * 100))
			avg.light.add(new(float64(deref(so.LightMin)) / total * 100))
			avg.rem.add(new(float64(deref(so.RemMin)) / total * 100))
		}
		if *so.SleepHours < 6 {
			short++
		}
		for name, q := range so.SubScores {
			if out.Qualifiers == nil {
				out.Qualifiers = map[string]map[string]int{}
			}
			if out.Qualifiers[name] == nil {
				out.Qualifiers[name] = map[string]int{}
			}
			out.Qualifiers[name][q]++
		}
	}
	out.Nights = len(nights)
	if out.Nights == 0 {
		out.Notes = append(out.Notes, "no sleep recorded in this period")
		return nil, out, nil
	}

	out.Averages = SleepAverages{
		SleepHours: avg.hours.value(2), SleepNeedHours: avg.need.value(2), Score: avg.score.value(0),
		AwakeCount: avg.awake.value(1), AvgHR: avg.hr.value(0), RestingHR: avg.rhr.value(0), HRV: avg.hrv.value(0),
		SleepStress: avg.stress.value(0), SpO2: avg.spo2.value(0), LowestSpO2: avg.low.value(0), Respiration: avg.resp.value(1),
	}
	if a, n := out.Averages.SleepHours, out.Averages.SleepNeedHours; a != nil && n != nil && *n > *a {
		out.Averages.DebtHoursWeek = new(round((*n-*a)*7, 1))
	}
	out.StageShare = SleepStageShare{Deep: avg.deep.value(0), Light: avg.light.value(0), REM: avg.rem.value(0)}
	out.Regularity.ShortNights = short

	var beds, wakes, wkBed, weBed []float64
	for _, n := range nights {
		if !n.hasTiming {
			continue
		}
		beds, wakes = append(beds, n.bedMin), append(wakes, n.wakeMin)
		// Nights before Saturday and Sunday (woke on Sat/Sun) are "free" nights.
		if wd := n.date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weBed = append(weBed, (n.bedMin+n.wakeMin)/2)
		} else {
			wkBed = append(wkBed, (n.bedMin+n.wakeMin)/2)
		}
	}
	if len(beds) >= 3 {
		out.Regularity.MedianBedtime = clockFrom18(median(beds))
		out.Regularity.MedianWake = clockFrom18(median(wakes))
		out.Regularity.BedtimeSpreadMin = new(round(stddev(beds), 0))
		out.Regularity.WakeSpreadMin = new(round(stddev(wakes), 0))
	}
	if len(wkBed) >= 2 && len(weBed) >= 1 {
		out.Regularity.SocialJetlagMin = new(round(math.Abs(median(weBed)-median(wkBed)), 0))
	}

	// Score trend: least-squares slope of score vs day index, per week.
	var xs, ys []float64
	for _, n := range nights {
		if n.s.Score != nil {
			xs, ys = append(xs, float64(from.DaysUntil(n.date))), append(ys, *n.s.Score)
		}
	}
	if len(xs) >= 7 {
		out.ScoreTrend = new(round(slope(xs, ys)*7, 1))
	}

	// Factors: compare score after days with a condition vs without.
	type cond struct {
		name string
		test func(day string) (bool, bool) // (applies, known)
	}
	conds := []cond{
		{"high stress day before (avg stress >= 40)", func(d string) (bool, bool) {
			s, ok := sums[d]
			if !ok || s.Stress.AvgLevel == nil {
				return false, false
			}
			return *s.Stress.AvgLevel >= 40, true
		}},
		{"workout ending after 20:00 the day before", func(d string) (bool, bool) { return lateWorkout[d], true }},
		{"hard workout the day before (training load >= 150)", func(d string) (bool, bool) { return hardDay[d], true }},
		{"low daytime activity the day before (< 5000 steps)", func(d string) (bool, bool) {
			s, ok := sums[d]
			if !ok || s.Movement.Steps.Value == nil {
				return false, false
			}
			return *s.Movement.Steps.Value < 5000, true
		}},
		{"bedtime more than 45 min later than median", nil},
	}
	medBed := 0.0
	if len(beds) > 0 {
		medBed = median(beds)
	}
	for _, c := range conds {
		var with, without mean
		for _, n := range nights {
			if n.s.Score == nil {
				continue
			}
			var applies, known bool
			if c.test == nil {
				applies, known = n.hasTiming && n.bedMin-medBed > 45, n.hasTiming
			} else {
				applies, known = c.test(n.date.AddDays(-1).String())
			}
			if !known {
				continue
			}
			if applies {
				with.add(n.s.Score)
			} else {
				without.add(n.s.Score)
			}
		}
		f := SleepFactor{Factor: c.name, NightsWith: with.n, ScoreWith: with.value(0), ScoreWithout: without.value(0)}
		if with.n == 0 {
			continue
		}
		if f.ScoreWith != nil && f.ScoreWithout != nil {
			f.Difference = new(round(*f.ScoreWith-*f.ScoreWithout, 0))
		}
		out.Factors = append(out.Factors, f)
	}

	scored := slices.DeleteFunc(slices.Clone(nights), func(n night) bool { return n.s.Score == nil })
	slices.SortFunc(scored, func(a, b night) int { return int(*a.s.Score - *b.s.Score) })
	for i := 0; i < min(3, len(scored)); i++ {
		out.Worst = append(out.Worst, scored[i].s)
		out.Best = append(out.Best, scored[len(scored)-1-i].s)
	}

	out.Notes = append(out.Notes,
		"Factors are simple before/after comparisons over this period, not proof of cause; with fewer than ~5 nights_with a difference is noise.",
		"Typical adult ranges for reference: 7-9 h sleep, deep 13-23%, REM 20-25%, bedtime spread under 30 min.",
	)
	if out.Averages.LowestSpO2 != nil && *out.Averages.LowestSpO2 < 88 {
		out.Notes = append(out.Notes, "Average lowest overnight SpO2 below 88%: wrist SpO2 is noisy, but repeated low values with snoring or daytime sleepiness are worth discussing with a doctor.")
	}
	return nil, out, nil
}

// sleepTiming returns bedtime and wake time as minutes since 18:00 local.
func sleepTiming(s garmin.DailySleep) (bed, wake float64, ok bool) {
	if s.SleepStartTimestampLocal == nil || s.SleepEndTimestampLocal == nil || *s.SleepStartTimestampLocal == 0 {
		return 0, 0, false
	}
	toMin := func(ms int64) float64 {
		t := time.UnixMilli(ms).UTC()
		m := float64(t.Hour()*60+t.Minute()) - 18*60
		if m < 0 {
			m += 24 * 60
		}
		return m
	}
	return toMin(*s.SleepStartTimestampLocal), toMin(*s.SleepEndTimestampLocal), true
}

func clockFrom18(m float64) string {
	total := (int(math.Round(m)) + 18*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
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

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	m := sum / float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(v)-1))
}

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

// --- garmin_trends ---

// TrendsInput selects the period and bucket size.
type TrendsInput struct {
	Days    int    `json:"days,omitempty" jsonschema:"Length of the period ending today, 14-731 days. Default 84 (12 weeks)."`
	EndDate string `json:"end_date,omitempty" jsonschema:"Last day, YYYY-MM-DD. Default: today."`
	Period  string `json:"period,omitempty" jsonschema:"Bucket size: week (Monday-based) or month. Default week."`
}

// TrendBucket holds averages for one week or month.
type TrendBucket struct {
	Start          string   `json:"start"`
	DaysWithData   int      `json:"days_with_data"`
	RestingHR      *float64 `json:"resting_hr_bpm,omitempty"`
	HRV            *float64 `json:"hrv_ms,omitempty"`
	SleepHours     *float64 `json:"sleep_hours,omitempty"`
	SleepScore     *float64 `json:"sleep_score,omitempty"`
	Steps          *float64 `json:"steps_per_day,omitempty"`
	Stress         *float64 `json:"stress_avg,omitempty"`
	BodyBatteryMax *float64 `json:"body_battery_max,omitempty"`
	IntensityMin   int      `json:"intensity_minutes_total"`
	Workouts       int      `json:"workouts"`
	WorkoutMin     int      `json:"workout_minutes"`
	TrainingLoad   int      `json:"training_load_total"`
	WeightKg       *float64 `json:"weight_kg,omitempty"`
}

// TrendsOut wraps the buckets.
type TrendsOut struct {
	From    string        `json:"from"`
	To      string        `json:"to"`
	Period  string        `json:"period"`
	Buckets []TrendBucket `json:"buckets"`
	Note    string        `json:"note"`
}

func (h *handlers) trends(ctx context.Context, _ *mcp.CallToolRequest, in TrendsInput) (*mcp.CallToolResult, TrendsOut, error) {
	period := strings.ToLower(strings.TrimSpace(in.Period))
	if period == "" {
		period = "week"
	}
	if period != "week" && period != "month" {
		return nil, TrendsOut{}, fmt.Errorf("period must be week or month, got %q", in.Period)
	}
	days := in.Days
	if days == 0 {
		days = 84
	}
	if days < 14 || days > 731 {
		return nil, TrendsOut{}, fmt.Errorf("days must be between 14 and 731, got %d", days)
	}
	from, to, err := h.resolve(RangeInput{EndDate: in.EndDate, Days: days}, days, 731)
	if err != nil {
		return nil, TrendsOut{}, err
	}

	var (
		wg                                sync.WaitGroup
		sleep                             map[string]garmin.DailySleep
		hrv                               map[string]garmin.HRVSummary
		sums                              map[string]garmin.DailySummary
		weight                            map[string]garmin.WeightSample
		acts                              []garmin.Activity
		eSleep, eHRV, eSum, eWeight, eAct error
	)
	wg.Go(func() { sleep, eSleep = h.svc.Sleep(ctx, from, to) })
	wg.Go(func() { hrv, eHRV = h.svc.HRV(ctx, from, to) })
	wg.Go(func() { sums, eSum = h.svc.DailySummaries(ctx, from, to) })
	wg.Go(func() { weight, eWeight = h.svc.Weight(ctx, from, to) })
	wg.Go(func() { acts, eAct = h.svc.Activities(ctx, from, to, "", "", 3000) })
	wg.Wait()
	for _, e := range []error{eSleep, eHRV, eSum, eWeight, eAct} {
		if e != nil {
			return nil, TrendsOut{}, e
		}
	}

	bucketOf := func(d civil.Date) civil.Date {
		if period == "month" {
			return d.Month()
		}
		return d.AddDays(-((int(d.Weekday()) + 6) % 7)) // Monday
	}
	type acc struct {
		b                                             TrendBucket
		rhr, hrv, sleepH, score, steps, stress, bb, w mean
	}
	accs := map[string]*acc{}
	var order []string
	get := func(d civil.Date) *acc {
		k := bucketOf(d).String()
		a, ok := accs[k]
		if !ok {
			a = &acc{b: TrendBucket{Start: k}}
			accs[k] = a
			order = append(order, k)
		}
		return a
	}
	for _, d := range civil.Range(from, to) {
		a := get(d)
		k := d.String()
		if s, ok := sums[k]; ok {
			a.b.DaysWithData++
			a.rhr.addInt(s.HeartRate.RestingValue)
			a.steps.addInt(s.Movement.Steps.Value)
			a.stress.addInt(s.Stress.AvgLevel)
			a.bb.addInt(s.BodyBattery.MaxValue)
			a.b.IntensityMin += deref(s.IntensityMinutes.Moderate) + 2*deref(s.IntensityMinutes.Vigorous)
		}
		if s, ok := sleep[k]; ok {
			so := sleepOut(s)
			a.sleepH.add(so.SleepHours)
			a.score.add(so.Score)
		}
		if v, ok := hrv[k]; ok {
			a.hrv.addInt(v.LastNightAvg)
		}
		if w, ok := weight[k]; ok {
			a.w.add(scaled(w.Weight, 0.001, 2))
		}
	}
	for _, act := range acts {
		d, err := civil.Parse(firstN(act.StartTimeLocal, 10))
		if err != nil || d.Before(from) || d.After(to) {
			continue
		}
		a := get(d)
		a.b.Workouts++
		if act.Duration != nil {
			a.b.WorkoutMin += int(math.Round(*act.Duration / 60))
		}
		if act.ActivityTrainingLoad != nil {
			a.b.TrainingLoad += int(math.Round(*act.ActivityTrainingLoad))
		}
	}
	out := TrendsOut{From: from.String(), To: to.String(), Period: period, Buckets: []TrendBucket{},
		Note: "Averages ignore days without data. intensity_minutes_total counts vigorous minutes twice (WHO convention, Garmin does the same). The first and last buckets may be partial: check days_with_data."}
	for _, k := range order {
		a := accs[k]
		a.b.RestingHR, a.b.HRV = a.rhr.value(1), a.hrv.value(1)
		a.b.SleepHours, a.b.SleepScore = a.sleepH.value(2), a.score.value(0)
		a.b.Steps, a.b.Stress, a.b.BodyBatteryMax = a.steps.value(0), a.stress.value(0), a.bb.value(0)
		a.b.WeightKg = a.w.value(1)
		out.Buckets = append(out.Buckets, a.b)
	}
	return nil, out, nil
}

// --- garmin_profile ---

// ProfileOut is user context.
type ProfileOut struct {
	TimeZoneGarmin    string                  `json:"time_zone_garmin,omitempty"`
	TimeZoneServer    string                  `json:"time_zone_server"`
	MeasurementSystem string                  `json:"measurement_system,omitempty"`
	HRZones           []garmin.HeartRateZones `json:"hr_zones,omitempty"`
	FitnessAge        *garmin.FitnessAge      `json:"fitness_age,omitempty"`
	LatestWeight      *WeightOut              `json:"latest_weight,omitempty"`
	Warnings          []string                `json:"warnings,omitempty"`
}

func (h *handlers) profile(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProfileOut, error) {
	today := h.svc.Today()
	var (
		wg              sync.WaitGroup
		p               *garmin.ProfileSettings
		z               []garmin.HeartRateZones
		fa              *garmin.FitnessAge
		w               map[string]garmin.WeightSample
		eP, eZ, eFA, eW error
	)
	wg.Go(func() { p, eP = h.svc.Profile(ctx) })
	wg.Go(func() { z, eZ = h.svc.HeartRateZones(ctx) })
	wg.Go(func() { fa, eFA = h.svc.FitnessAge(ctx, today) })
	wg.Go(func() { w, eW = h.svc.Weight(ctx, today.AddDays(-89), today) })
	wg.Wait()
	if eP != nil {
		return nil, ProfileOut{}, eP
	}
	out := ProfileOut{TimeZoneServer: h.svc.Location().String(), TimeZoneGarmin: p.TimeZone, MeasurementSystem: p.MeasurementSystem}
	if p.TimeZone != "" {
		if loc, err := time.LoadLocation(p.TimeZone); err == nil {
			_, a := time.Now().In(loc).Zone()
			_, b := time.Now().In(h.svc.Location()).Zone()
			if a != b {
				out.Warnings = append(out.Warnings, fmt.Sprintf("server time zone %s differs from Garmin profile %s: set GARMIN_TZ=%s so 'today' matches your watch", h.svc.Location(), p.TimeZone, p.TimeZone))
			}
		}
	}
	if eZ == nil {
		out.HRZones = z
	} else {
		out.Warnings = append(out.Warnings, "heart rate zones unavailable: "+eZ.Error())
	}
	if eFA == nil {
		out.FitnessAge = fa
	} else {
		out.Warnings = append(out.Warnings, "fitness age unavailable: "+eFA.Error())
	}
	if eW == nil && len(w) > 0 {
		keys := sortedKeys(w)
		last := keys[len(keys)-1]
		wo := weightOut(last, w[last])
		out.LatestWeight = &wo
	}
	return nil, out, nil
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
