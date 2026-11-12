package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
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
	needMin   *float64 // Garmin's baseline sleep need
	onset     float64  // minutes since 18:00 local
	wake      float64
	hasTiming bool
	hrv       *float64
}

func (h *handlers) sleepAnalysis(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, SleepAnalysisOut, error) {
	from, to, err := h.resolve(in, 28, 366)
	if err != nil {
		return nil, SleepAnalysisOut{}, err
	}
	var (
		sleep map[string]garmin.DailySleep
		hrv   map[string]garmin.HRVSummary
		sums  map[string]garmin.DailySummary
		acts  []garmin.Activity
	)
	prev := from.AddDays(-1) // the day before the first night
	err = fetchAll(
		func() (err error) { sleep, err = h.svc.Sleep(ctx, from, to); return },
		func() (err error) { hrv, err = h.svc.HRV(ctx, from, to); return },
		func() (err error) { sums, err = h.svc.DailySummaries(ctx, prev, to); return },
		func() (err error) {
			acts, err = h.svc.Activities(ctx, prev, to, "", "", maxActivitiesPerAnalysis)
			return
		},
	)
	if err != nil {
		return nil, SleepAnalysisOut{}, err
	}

	out := SleepAnalysisOut{From: from.String(), To: to.String(), Factors: []SleepFactor{},
		Worst: []SleepOut{}, Best: []SleepOut{}, Notes: []string{}}
	nights := buildNights(sleep, hrv)
	if out.Nights = len(nights); out.Nights == 0 {
		out.Notes = append(out.Notes, "no sleep recorded in this period")
		return nil, out, nil
	}
	out.Averages = sleepAverages(nights, sums)
	out.StageShare = stageShare(nights)
	out.Qualifiers = subScoreCounts(nights)
	out.Regularity = regularity(nights)
	out.ScoreTrend, out.ScoreTrendN = scoreTrend(nights, from)
	out.Factors = sleepFactors(nights, sums, acts)
	out.Worst, out.Best = worstBest(nights)
	out.Notes = sleepNotes(out.Averages)
	return nil, out, nil
}

// buildNights returns the nights with recorded sleep, oldest first.
func buildNights(sleep map[string]garmin.DailySleep, hrv map[string]garmin.HRVSummary) []night {
	var nights []night
	for _, k := range sortedKeys(sleep) {
		raw := sleep[k]
		d, err := civil.Parse(k)
		n := night{date: d, s: sleepOut(raw), hrv: intToF(hrv[k].LastNightAvg)}
		if err != nil || n.s.SleepHours == nil {
			continue
		}
		// Garmin's baseline need, not the adjusted "actual" need, which
		// already adds extra need after short nights and would double count.
		if raw.SleepNeed != nil && raw.SleepNeed.Baseline != nil && *raw.SleepNeed.Baseline > 0 {
			n.needMin = intToF(raw.SleepNeed.Baseline)
		}
		n.onset, n.wake, n.hasTiming = sleepTiming(raw)
		nights = append(nights, n)
	}
	return nights
}

func sleepAverages(nights []night, sums map[string]garmin.DailySummary) SleepAverages {
	var a struct{ hours, need, vsNeed, score, awake, hr, rhr, hrv, stress, spo2, low, resp mean }
	for _, n := range nights {
		a.hours.add(n.s.SleepHours)
		if n.needMin != nil {
			a.need.add(new(*n.needMin / 60))
			a.vsNeed.add(new(*n.s.SleepHours*60 - *n.needMin))
		}
		a.score.add(n.s.Score)
		a.awake.addInt(n.s.AwakeCount)
		a.hr.add(n.s.AvgHR)
		a.stress.add(n.s.AvgStress)
		a.spo2.add(n.s.AvgSpO2)
		a.low.add(n.s.LowestSpO2)
		a.resp.add(n.s.AvgRespiration)
		a.hrv.add(n.hrv)
		a.rhr.addInt(sums[n.date.String()].HeartRate.RestingValue)
	}
	avg := SleepAverages{
		SleepHours: a.hours.value(2), SleepNeedHours: a.need.value(2), VsNeedMin: a.vsNeed.value(0),
		Score: a.score.value(0), AwakeCount: a.awake.value(1), AvgHR: a.hr.value(0), RestingHR: a.rhr.value(0),
		HRV: a.hrv.value(0), SleepStress: a.stress.value(0), SpO2: a.spo2.value(0), LowestSpO2: a.low.value(0),
		Respiration: a.resp.value(1),
	}
	if avg.VsNeedMin != nil {
		avg.WeeklyShortfallH = new(round(math.Max(0, -*avg.VsNeedMin)*7/60, 1))
	}
	return avg
}

// stageShare averages stage percentages over nights where all three stages were measured.
func stageShare(nights []night) SleepStageShare {
	var deep, light, rem mean
	for _, n := range nights {
		s := n.s
		if s.DeepMin == nil || s.LightMin == nil || s.RemMin == nil {
			continue
		}
		if total := float64(*s.DeepMin + *s.LightMin + *s.RemMin); total > 0 {
			deep.add(new(float64(*s.DeepMin) / total * 100))
			light.add(new(float64(*s.LightMin) / total * 100))
			rem.add(new(float64(*s.RemMin) / total * 100))
		}
	}
	return SleepStageShare{Deep: deep.value(0), Light: light.value(0), REM: rem.value(0), Nights: deep.n}
}

func subScoreCounts(nights []night) map[string]map[string]int {
	var counts map[string]map[string]int
	for _, n := range nights {
		for name, q := range n.s.SubScores {
			if counts == nil {
				counts = map[string]map[string]int{}
			}
			if counts[name] == nil {
				counts[name] = map[string]int{}
			}
			counts[name][q]++
		}
	}
	return counts
}

func regularity(nights []night) SleepRegularity {
	var r SleepRegularity
	var onsets, wakes, workMid, freeMid []float64
	for _, n := range nights {
		if *n.s.SleepHours < 6 {
			r.ShortNights++
		}
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
		r.MedianOnset, r.MedianWake = clockFrom18(median(onsets)), clockFrom18(median(wakes))
		r.OnsetSpreadMin, r.WakeSpreadMin = new(round(stddev(onsets), 0)), new(round(stddev(wakes), 0))
	}
	if len(workMid) >= 2 && len(freeMid) >= 2 {
		r.SocialJetlagMin = new(round(math.Abs(median(freeMid)-median(workMid)), 0))
	}
	return r
}

// scoreTrend is the least-squares slope of the sleep score per week; it
// needs at least 14 scored nights.
func scoreTrend(nights []night, from civil.Date) (*float64, int) {
	var xs, ys []float64
	for _, n := range nights {
		if n.s.Score != nil {
			xs, ys = append(xs, float64(from.DaysUntil(n.date))), append(ys, *n.s.Score)
		}
	}
	if len(xs) < 14 {
		return nil, len(xs)
	}
	return new(round(slope(xs, ys)*7, 1)), len(xs)
}

func worstBest(nights []night) (worst, best []SleepOut) {
	scored := slices.DeleteFunc(slices.Clone(nights), func(n night) bool { return n.s.Score == nil })
	slices.SortFunc(scored, func(a, b night) int { return cmp.Compare(*a.s.Score, *b.s.Score) })
	worst, best = []SleepOut{}, []SleepOut{}
	for i := range min(3, len(scored)/2) { // never list a night as both worst and best
		worst = append(worst, scored[i].s)
		best = append(best, scored[len(scored)-1-i].s)
	}
	return worst, best
}

func sleepNotes(avg SleepAverages) []string {
	notes := []string{
		"Sleep stages, SpO2, HRV and breathing are wrist-sensor estimates: trust patterns over several nights, not single values.",
		fmt.Sprintf("Factors compare nights after days with and without a condition; differences are shown only with >= %d nights on each side and are associations, not proof of cause. Daily stress includes the evening before sleep, so stress and sleep partly overlap.", minFactorNights),
		"Approximate population references: adults 7-9 h of sleep; deep sleep ~13-23% and REM ~20-25% of sleep time. A bedtime spread under ~30 min is a common heuristic for regularity, not a clinical norm.",
	}
	if avg.LowestSpO2 != nil && *avg.LowestSpO2 < 88 {
		notes = append(notes, "Average lowest overnight SpO2 is below 88%. Wrist SpO2 is noisy, but repeated low values together with snoring, gasping or daytime sleepiness are worth discussing with a doctor.")
	}
	return notes
}

// factorCond is one condition of the previous day (or of the night itself).
type factorCond struct {
	name string
	test func(n night, dayBefore string) (applies, known bool)
}

// sleepFactors compares nights after days with and without each condition.
func sleepFactors(nights []night, sums map[string]garmin.DailySummary, acts []garmin.Activity) []SleepFactor {
	out := []SleepFactor{}
	for _, c := range factorConds(nights, sums, acts) {
		var sides [2]struct {
			nights           int
			score, hrv, hrSl mean
		}
		for _, n := range nights {
			applies, known := c.test(n, n.date.AddDays(-1).String())
			if !known {
				continue
			}
			sd := &sides[0]
			if applies {
				sd = &sides[1]
			}
			sd.nights++
			sd.score.add(n.s.Score)
			sd.hrv.add(n.hrv)
			sd.hrSl.add(n.s.AvgHR)
		}
		with, without := sides[1], sides[0]
		if with.nights == 0 {
			continue
		}
		f := SleepFactor{Factor: c.name, NightsWith: with.nights, NightsWithout: without.nights}
		// Gate on scored nights: the comparison is about the sleep score.
		if with.score.n >= minFactorNights && without.score.n >= minFactorNights {
			f.ScoreWith, f.ScoreWithout = with.score.value(0), without.score.value(0)
			f.ScoreDiff = new(round(*f.ScoreWith-*f.ScoreWithout, 0))
			f.HRVWith, f.HRVWithout = with.hrv.value(0), without.hrv.value(0)
			f.SleepHRWith, f.SleepHRWithout = with.hrSl.value(0), without.hrSl.value(0)
		}
		out = append(out, f)
	}
	return out
}

// factorConds builds the conditions; thresholds are relative to the user's own period.
func factorConds(nights []night, sums map[string]garmin.DailySummary, acts []garmin.Activity) []factorCond {
	late, dayLoad := activityFacts(acts)
	var stressDays, loads, onsets []float64
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
	for _, n := range nights {
		if n.hasTiming {
			onsets = append(onsets, n.onset)
		}
	}

	var conds []factorCond
	if len(stressDays) >= 10 {
		p75 := percentile(stressDays, 75)
		conds = append(conds, factorCond{fmt.Sprintf("stress above your usual the day before (top 25%% of your days, avg >= %.0f)", p75),
			func(_ night, d string) (bool, bool) {
				v := sums[d].Stress.AvgLevel
				return v != nil && float64(*v) >= p75, v != nil
			}})
	}
	conds = append(conds, factorCond{"workout ending after 20:00 the day before", func(_ night, d string) (bool, bool) { return late[d], true }})
	if len(loads) >= 5 {
		p75 := percentile(loads, 75)
		conds = append(conds, factorCond{fmt.Sprintf("hard training day before (your top 25%% of training days, Garmin load >= %.0f)", p75),
			func(_ night, d string) (bool, bool) { return dayLoad[d] >= p75, true }})
	}
	conds = append(conds, factorCond{"low daytime activity the day before (< 5000 steps)", func(_ night, d string) (bool, bool) {
		v := sums[d].Movement.Steps.Value
		return v != nil && *v < 5000, v != nil
	}})
	if len(onsets) >= 7 {
		med := median(onsets)
		conds = append(conds, factorCond{"sleep onset more than 45 min later than your median", func(n night, _ string) (bool, bool) {
			return n.hasTiming && n.onset-med > 45, n.hasTiming
		}})
	}
	return conds
}

// activityFacts returns, per local date, whether a workout ended after 20:00
// (or past midnight) and the summed Garmin training load.
func activityFacts(acts []garmin.Activity) (late map[string]bool, load map[string]float64) {
	late, load = map[string]bool{}, map[string]float64{}
	for _, a := range acts {
		date := firstN(a.StartTimeLocal, 10)
		if a.ActivityTrainingLoad != nil {
			load[date] += *a.ActivityTrainingLoad
		}
		t, err := time.Parse(time.DateTime, firstN(a.StartTimeLocal, 19))
		if err != nil || a.Duration == nil {
			continue
		}
		if end := t.Add(time.Duration(*a.Duration * float64(time.Second))); end.Hour() >= 20 || civil.Of(end).String() != date {
			late[date] = true
		}
	}
	return late, load
}
