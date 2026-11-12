package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// minFactorNights is the smallest group for which a factor difference is shown.
const minFactorNights = 5

// SleepAnalysisOut summarises sleep quality and its likely drivers.
type SleepAnalysisOut struct {
	From        string                    `json:"from"`
	To          string                    `json:"to"`
	Nights      int                       `json:"nights_with_data"`
	Averages    SleepAverages             `json:"averages"`
	Regularity  SleepRegularity           `json:"regularity"`
	StageShare  SleepStageShare           `json:"stage_share_pct"`
	Qualifiers  map[string]map[string]int `json:"sub_score_counts,omitempty"`
	ScoreTrend  *float64                  `json:"score_trend_per_week,omitempty"`
	ScoreTrendN int                       `json:"score_trend_nights"`
	Factors     []SleepFactor             `json:"factors"`
	Worst       []SleepOut                `json:"worst_nights"`
	Best        []SleepOut                `json:"best_nights"`
	Notes       []string                  `json:"notes"`
}

// SleepAverages are means over the period.
type SleepAverages struct {
	SleepHours       *float64 `json:"sleep_hours,omitempty"`
	SleepNeedHours   *float64 `json:"sleep_need_baseline_hours,omitempty"`
	VsNeedMin        *float64 `json:"avg_sleep_vs_need_min,omitempty"`
	WeeklyShortfallH *float64 `json:"weekly_shortfall_hours,omitempty"`
	Score            *float64 `json:"score,omitempty"`
	AwakeCount       *float64 `json:"awake_count,omitempty"`
	AvgHR            *float64 `json:"sleep_hr_bpm,omitempty"`
	RestingHR        *float64 `json:"resting_hr_bpm,omitempty"`
	HRV              *float64 `json:"hrv_ms,omitempty"`
	SleepStress      *float64 `json:"sleep_stress,omitempty"`
	SpO2             *float64 `json:"spo2_pct,omitempty"`
	LowestSpO2       *float64 `json:"lowest_spo2_pct,omitempty"`
	Respiration      *float64 `json:"respiration_brpm,omitempty"`
}

// SleepRegularity describes the timing of sleep.
type SleepRegularity struct {
	MedianOnset     string   `json:"median_sleep_onset,omitempty"`
	MedianWake      string   `json:"median_wake_time,omitempty"`
	OnsetSpreadMin  *float64 `json:"sleep_onset_stddev_min,omitempty"`
	WakeSpreadMin   *float64 `json:"wake_stddev_min,omitempty"`
	SocialJetlagMin *float64 `json:"social_jetlag_min,omitempty"`
	ShortNights     int      `json:"nights_under_6h"`
}

// SleepStageShare is the mean share of each stage in total sleep, over nights
// where all three stages were measured.
type SleepStageShare struct {
	Deep   *float64 `json:"deep,omitempty"`
	Light  *float64 `json:"light,omitempty"`
	REM    *float64 `json:"rem,omitempty"`
	Nights int      `json:"nights"`
}

// SleepFactor compares nights after days with and without a condition.
type SleepFactor struct {
	Factor         string   `json:"factor"`
	NightsWith     int      `json:"nights_with"`
	NightsWithout  int      `json:"nights_without"`
	ScoreWith      *float64 `json:"avg_score_with,omitempty"`
	ScoreWithout   *float64 `json:"avg_score_without,omitempty"`
	ScoreDiff      *float64 `json:"score_difference,omitempty"`
	HRVWith        *float64 `json:"avg_hrv_with_ms,omitempty"`
	HRVWithout     *float64 `json:"avg_hrv_without_ms,omitempty"`
	SleepHRWith    *float64 `json:"avg_sleep_hr_with,omitempty"`
	SleepHRWithout *float64 `json:"avg_sleep_hr_without,omitempty"`
}

type night struct {
	date      civil.Date
	s         SleepOut
	onset     float64 // minutes since 18:00 local
	wake      float64
	hasTiming bool
	hrv       *float64
}

func (h *handlers) sleepAnalysis(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, SleepAnalysisOut, error) {
	from, to, err := h.resolve(in, 28, 366)
	if err != nil {
		return nil, SleepAnalysisOut{}, err
	}
	prev := from.AddDays(-1) // the day before the first night
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

	out := SleepAnalysisOut{From: from.String(), To: to.String(), Factors: []SleepFactor{},
		Worst: []SleepOut{}, Best: []SleepOut{}, Notes: []string{}}

	nights, avg := collectNights(sleep, hrv, sums, &out)
	out.Nights = len(nights)
	if out.Nights == 0 {
		out.Notes = append(out.Notes, "no sleep recorded in this period")
		return nil, out, nil
	}
	out.Averages = avg

	var onsets, wakes, workMid, freeMid []float64
	for _, n := range nights {
		if !n.hasTiming {
			continue
		}
		onsets, wakes = append(onsets, n.onset), append(wakes, n.wake)
		// Nights ending on Saturday or Sunday are "free" nights.
		if wd := n.date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			freeMid = append(freeMid, (n.onset+n.wake)/2)
		} else {
			workMid = append(workMid, (n.onset+n.wake)/2)
		}
	}
	if len(onsets) >= 3 {
		out.Regularity.MedianOnset = clockFrom18(median(onsets))
		out.Regularity.MedianWake = clockFrom18(median(wakes))
		out.Regularity.OnsetSpreadMin = new(round(stddev(onsets), 0))
		out.Regularity.WakeSpreadMin = new(round(stddev(wakes), 0))
	}
	if len(workMid) >= 2 && len(freeMid) >= 2 {
		out.Regularity.SocialJetlagMin = new(round(math.Abs(median(freeMid)-median(workMid)), 0))
	}

	// Score trend: least-squares slope per week, only with enough nights.
	var xs, ys []float64
	for _, n := range nights {
		if n.s.Score != nil {
			xs, ys = append(xs, float64(from.DaysUntil(n.date))), append(ys, *n.s.Score)
		}
	}
	out.ScoreTrendN = len(xs)
	if len(xs) >= 14 {
		out.ScoreTrend = new(round(slope(xs, ys)*7, 1))
	}

	out.Factors = sleepFactors(nights, sums, acts, onsets)

	scored := slices.DeleteFunc(slices.Clone(nights), func(n night) bool { return n.s.Score == nil })
	slices.SortFunc(scored, func(a, b night) int { return cmp.Compare(*a.s.Score, *b.s.Score) })
	k := min(3, len(scored)/2) // never list the same night as both worst and best
	for i := range k {
		out.Worst = append(out.Worst, scored[i].s)
		out.Best = append(out.Best, scored[len(scored)-1-i].s)
	}

	out.Notes = append(out.Notes,
		"Sleep stages, SpO2, HRV and breathing are wrist-sensor estimates: trust patterns over several nights, not single values.",
		fmt.Sprintf("Factors compare nights after days with and without a condition; differences are shown only with >= %d nights on each side and are associations, not proof of cause. Daily stress includes the evening before sleep, so stress and sleep partly overlap.", minFactorNights),
		"Approximate population references: adults 7-9 h of sleep; deep sleep ~13-23% and REM ~20-25% of sleep time. A bedtime spread under ~30 min is a common heuristic for regularity, not a clinical norm.",
	)
	if out.Averages.LowestSpO2 != nil && *out.Averages.LowestSpO2 < 88 {
		out.Notes = append(out.Notes, "Average lowest overnight SpO2 is below 88%. Wrist SpO2 is noisy, but repeated low values together with snoring, gasping or daytime sleepiness are worth discussing with a doctor.")
	}
	return nil, out, nil
}

// collectNights builds per-night records and period averages.
func collectNights(sleep map[string]garmin.DailySleep, hrv map[string]garmin.HRVSummary, sums map[string]garmin.DailySummary, out *SleepAnalysisOut) ([]night, SleepAverages) {
	var nights []night
	var a struct{ hours, need, vsNeed, score, awake, hr, rhr, hrv, stress, spo2, low, resp, deep, light, rem mean }
	for _, k := range sortedKeys(sleep) {
		raw := sleep[k]
		so := sleepOut(raw)
		if so.SleepHours == nil {
			continue
		}
		d, err := civil.Parse(k)
		if err != nil {
			continue
		}
		n := night{date: d, s: so}
		n.onset, n.wake, n.hasTiming = sleepTiming(raw)
		if v, ok := hrv[k]; ok {
			n.hrv = intToF(v.LastNightAvg)
		}
		nights = append(nights, n)

		a.hours.add(so.SleepHours)
		// Garmin's baseline need, not the adjusted "actual" need, which
		// already adds extra need after short nights and would double count.
		if raw.SleepNeed != nil && raw.SleepNeed.Baseline != nil && *raw.SleepNeed.Baseline > 0 {
			need := float64(*raw.SleepNeed.Baseline)
			a.need.add(new(need / 60))
			a.vsNeed.add(new(*so.SleepHours*60 - need))
		}
		a.score.add(so.Score)
		a.awake.addInt(so.AwakeCount)
		a.hr.add(so.AvgHR)
		a.stress.add(so.AvgStress)
		a.spo2.add(so.AvgSpO2)
		a.low.add(so.LowestSpO2)
		a.resp.add(so.AvgRespiration)
		a.hrv.add(n.hrv)
		if s, ok := sums[k]; ok {
			a.rhr.addInt(s.HeartRate.RestingValue)
		}
		if so.DeepMin != nil && so.LightMin != nil && so.RemMin != nil {
			if total := float64(*so.DeepMin + *so.LightMin + *so.RemMin); total > 0 {
				a.deep.add(new(float64(*so.DeepMin) / total * 100))
				a.light.add(new(float64(*so.LightMin) / total * 100))
				a.rem.add(new(float64(*so.RemMin) / total * 100))
			}
		}
		if *so.SleepHours < 6 {
			out.Regularity.ShortNights++
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
	out.StageShare = SleepStageShare{Deep: a.deep.value(0), Light: a.light.value(0), REM: a.rem.value(0), Nights: a.deep.n}
	avg := SleepAverages{
		SleepHours: a.hours.value(2), SleepNeedHours: a.need.value(2), VsNeedMin: a.vsNeed.value(0),
		Score: a.score.value(0), AwakeCount: a.awake.value(1), AvgHR: a.hr.value(0), RestingHR: a.rhr.value(0),
		HRV: a.hrv.value(0), SleepStress: a.stress.value(0), SpO2: a.spo2.value(0), LowestSpO2: a.low.value(0),
		Respiration: a.resp.value(1),
	}
	if avg.VsNeedMin != nil {
		avg.WeeklyShortfallH = new(round(math.Max(0, -*avg.VsNeedMin)*7/60, 1))
	}
	return nights, avg
}

// sleepFactors compares nights after days with and without each condition.
func sleepFactors(nights []night, sums map[string]garmin.DailySummary, acts []garmin.Activity, onsets []float64) []SleepFactor {
	lateWorkout := map[string]bool{}
	dayLoad := map[string]float64{}
	for _, a := range acts {
		date := firstN(a.StartTimeLocal, 10)
		if a.ActivityTrainingLoad != nil {
			dayLoad[date] += *a.ActivityTrainingLoad
		}
		t, err := time.Parse(time.DateTime, firstN(a.StartTimeLocal, 19))
		if err != nil || a.Duration == nil {
			continue
		}
		if end := t.Add(time.Duration(*a.Duration * float64(time.Second))); end.Hour() >= 20 || civil.Of(end).String() != date {
			lateWorkout[date] = true
		}
	}

	// Personal thresholds from this period.
	var stressDays, loads []float64
	for _, s := range sums {
		if s.Stress.AvgLevel != nil {
			stressDays = append(stressDays, float64(*s.Stress.AvgLevel))
		}
	}
	for _, l := range dayLoad {
		if l > 0 {
			loads = append(loads, l)
		}
	}

	type cond struct {
		name string
		test func(n night, dayBefore string) (applies, known bool)
	}
	var conds []cond
	if len(stressDays) >= 10 {
		p75 := percentile(stressDays, 75)
		conds = append(conds, cond{fmt.Sprintf("stress above your usual the day before (top 25%% of your days, avg >= %.0f)", p75),
			func(_ night, d string) (bool, bool) {
				s, ok := sums[d]
				if !ok || s.Stress.AvgLevel == nil {
					return false, false
				}
				return float64(*s.Stress.AvgLevel) >= p75, true
			}})
	}
	conds = append(conds, cond{"workout ending after 20:00 the day before", func(_ night, d string) (bool, bool) { return lateWorkout[d], true }})
	if len(loads) >= 5 {
		p75 := percentile(loads, 75)
		conds = append(conds, cond{fmt.Sprintf("hard training day before (your top 25%% of training days, Garmin load >= %.0f)", p75),
			func(_ night, d string) (bool, bool) { return dayLoad[d] >= p75, true }})
	}
	conds = append(conds, cond{"low daytime activity the day before (< 5000 steps)", func(_ night, d string) (bool, bool) {
		s, ok := sums[d]
		if !ok || s.Movement.Steps.Value == nil {
			return false, false
		}
		return *s.Movement.Steps.Value < 5000, true
	}})
	if len(onsets) >= 7 {
		med := median(onsets)
		conds = append(conds, cond{"sleep onset more than 45 min later than your median", func(n night, _ string) (bool, bool) {
			return n.hasTiming && n.onset-med > 45, n.hasTiming
		}})
	}

	out := []SleepFactor{}
	for _, c := range conds {
		var sw, swo, hw, hwo, rw, rwo mean
		for _, n := range nights {
			applies, known := c.test(n, n.date.AddDays(-1).String())
			if !known {
				continue
			}
			if applies {
				sw.add(n.s.Score)
				hw.add(n.hrv)
				rw.add(n.s.AvgHR)
			} else {
				swo.add(n.s.Score)
				hwo.add(n.hrv)
				rwo.add(n.s.AvgHR)
			}
		}
		if sw.n == 0 {
			continue
		}
		f := SleepFactor{Factor: c.name, NightsWith: sw.n, NightsWithout: swo.n}
		if sw.n >= minFactorNights && swo.n >= minFactorNights {
			f.ScoreWith, f.ScoreWithout = sw.value(0), swo.value(0)
			f.ScoreDiff = new(round(*f.ScoreWith-*f.ScoreWithout, 0))
			f.HRVWith, f.HRVWithout = hw.value(0), hwo.value(0)
			f.SleepHRWith, f.SleepHRWithout = rw.value(0), rwo.value(0)
		}
		out = append(out, f)
	}
	return out
}
