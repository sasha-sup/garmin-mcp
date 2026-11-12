package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// --- garmin_daily_summary ---

// DayOut is one day of wellness totals.
type DayOut struct {
	Date                 string   `json:"date"`
	Steps                *int     `json:"steps,omitempty"`
	StepGoal             *int     `json:"step_goal,omitempty"`
	DistanceKm           *float64 `json:"distance_km,omitempty"`
	FloorsClimbed        *float64 `json:"floors_climbed,omitempty"`
	CaloriesTotal        *float64 `json:"calories_total_kcal,omitempty"`
	CaloriesActive       *float64 `json:"calories_active_kcal,omitempty"`
	RestingHR            *int     `json:"resting_hr_bpm,omitempty"`
	MinHR                *int     `json:"min_hr_bpm,omitempty"`
	MaxHR                *int     `json:"max_hr_bpm,omitempty"`
	IntensityModerateMin *int     `json:"intensity_moderate_min,omitempty"`
	IntensityVigorousMin *int     `json:"intensity_vigorous_min,omitempty"`
	StressAvg            *int     `json:"stress_avg,omitempty"`
	StressMax            *int     `json:"stress_max,omitempty"`
	StressQualifier      string   `json:"stress_qualifier,omitempty"`
	HighStressMin        *int     `json:"high_stress_min,omitempty"`
	RestMin              *int     `json:"rest_min,omitempty"`
	BodyBatteryMin       *int     `json:"body_battery_min,omitempty"`
	BodyBatteryMax       *int     `json:"body_battery_max,omitempty"`
	BodyBatteryCharged   *int     `json:"body_battery_charged,omitempty"`
	BodyBatteryDrained   *int     `json:"body_battery_drained,omitempty"`
	RespirationAvg       *float64 `json:"respiration_avg_brpm,omitempty"`
	SpO2Avg              *float64 `json:"spo2_avg_pct,omitempty"`
}

// DaysOut wraps a list of days.
type DaysOut struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Days []DayOut `json:"days"`
}

func (h *handlers) dailySummary(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, DaysOut, error) {
	from, to, err := h.resolve(in, 7, 366)
	if err != nil {
		return nil, DaysOut{}, err
	}
	m, err := h.svc.DailySummaries(ctx, from, to)
	if err != nil {
		return nil, DaysOut{}, err
	}
	out := DaysOut{From: from.String(), To: to.String(), Days: []DayOut{}}
	for _, k := range sortedKeys(m) {
		out.Days = append(out.Days, dayOut(m[k]))
	}
	return nil, out, nil
}

func dayOut(d garmin.DailySummary) DayOut {
	return DayOut{
		Date:                 d.CalendarDate,
		Steps:                d.Movement.Steps.Value,
		StepGoal:             d.Movement.Steps.Goal,
		DistanceKm:           scaled(d.Movement.Steps.DistanceInMeters, 0.001, 2),
		FloorsClimbed:        rp(d.Movement.FloorsAscended.Value, 0),
		CaloriesTotal:        rp(d.Calories.BurnedTotal, 0),
		CaloriesActive:       rp(d.Calories.BurnedActive, 0),
		RestingHR:            d.HeartRate.RestingValue,
		MinHR:                d.HeartRate.MinValue,
		MaxHR:                d.HeartRate.MaxValue,
		IntensityModerateMin: d.IntensityMinutes.Moderate,
		IntensityVigorousMin: d.IntensityMinutes.Vigorous,
		StressAvg:            d.Stress.AvgLevel,
		StressMax:            d.Stress.MaxLevel,
		StressQualifier:      d.Stress.Qualifier,
		HighStressMin:        msToMin(d.Stress.HighStressDurationInMillis),
		RestMin:              msToMin(d.Stress.RestDurationInMillis),
		BodyBatteryMin:       d.BodyBattery.MinValue,
		BodyBatteryMax:       d.BodyBattery.MaxValue,
		BodyBatteryCharged:   d.BodyBattery.ChargedValue,
		BodyBatteryDrained:   d.BodyBattery.DrainedValue,
		RespirationAvg:       rp(d.Respiration.AvgValue, 1),
		SpO2Avg:              rp(d.PulseOx.AvgValue, 1),
	}
}

// --- garmin_sleep ---

// SleepOut is one night.
type SleepOut struct {
	Date                string            `json:"date"`
	Bedtime             string            `json:"bedtime,omitempty"`
	WakeTime            string            `json:"wake_time,omitempty"`
	SleepHours          *float64          `json:"sleep_hours,omitempty"`
	DeepMin             *int              `json:"deep_min,omitempty"`
	LightMin            *int              `json:"light_min,omitempty"`
	RemMin              *int              `json:"rem_min,omitempty"`
	AwakeMin            *int              `json:"awake_min,omitempty"`
	NapMin              *int              `json:"nap_min,omitempty"`
	Score               *float64          `json:"score,omitempty"`
	ScoreQualifier      string            `json:"score_qualifier,omitempty"`
	SubScores           map[string]string `json:"sub_scores,omitempty"`
	AwakeCount          *int              `json:"awake_count,omitempty"`
	AvgHR               *float64          `json:"avg_hr_bpm,omitempty"`
	AvgStress           *float64          `json:"avg_stress,omitempty"`
	AvgSpO2             *float64          `json:"avg_spo2_pct,omitempty"`
	LowestSpO2          *float64          `json:"lowest_spo2_pct,omitempty"`
	AvgRespiration      *float64          `json:"avg_respiration_brpm,omitempty"`
	SleepNeedMin        *int              `json:"sleep_need_min,omitempty"`
	Feedback            string            `json:"feedback,omitempty"`
	Insight             string            `json:"insight,omitempty"`
	BreathingDisruption string            `json:"breathing_disruption,omitempty"`
}

// SleepListOut wraps nights.
type SleepListOut struct {
	From   string     `json:"from"`
	To     string     `json:"to"`
	Nights []SleepOut `json:"nights"`
}

func (h *handlers) sleep(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, SleepListOut, error) {
	from, to, err := h.resolve(in, 7, 366)
	if err != nil {
		return nil, SleepListOut{}, err
	}
	m, err := h.svc.Sleep(ctx, from, to)
	if err != nil {
		return nil, SleepListOut{}, err
	}
	out := SleepListOut{From: from.String(), To: to.String(), Nights: []SleepOut{}}
	for _, k := range sortedKeys(m) {
		if s := sleepOut(m[k]); s.SleepHours != nil {
			out.Nights = append(out.Nights, s)
		}
	}
	return nil, out, nil
}

func sleepOut(s garmin.DailySleep) SleepOut {
	o := SleepOut{
		Date:                s.CalendarDate,
		Bedtime:             localClock(s.SleepStartTimestampLocal),
		WakeTime:            localClock(s.SleepEndTimestampLocal),
		DeepMin:             secToMin(s.DeepSleepSeconds),
		LightMin:            secToMin(s.LightSleepSeconds),
		RemMin:              secToMin(s.RemSleepSeconds),
		AwakeMin:            secToMin(s.AwakeSleepSeconds),
		NapMin:              secToMin(s.NapTimeSeconds),
		AwakeCount:          s.AwakeCount,
		AvgHR:               rp(s.AvgHeartRate, 0),
		AvgStress:           rp(s.AvgSleepStress, 0),
		AvgSpO2:             rp(s.AverageSpO2Value, 0),
		LowestSpO2:          rp(s.LowestSpO2Value, 0),
		AvgRespiration:      rp(s.AverageRespirationValue, 1),
		Feedback:            s.SleepScoreFeedback,
		Insight:             s.SleepScoreInsight,
		BreathingDisruption: s.BreathingDisruption,
	}
	if s.SleepTimeSeconds != nil && *s.SleepTimeSeconds > 0 {
		h := round(float64(*s.SleepTimeSeconds)/3600, 2)
		o.SleepHours = &h
	}
	if o.NapMin != nil && *o.NapMin == 0 {
		o.NapMin = nil
	}
	if s.SleepNeed != nil {
		o.SleepNeedMin = s.SleepNeed.Actual
	}
	for name, sc := range s.SleepScores {
		if name == "overall" {
			o.Score, o.ScoreQualifier = sc.Value, sc.QualifierKey
			continue
		}
		if sc.QualifierKey == "" {
			continue
		}
		if o.SubScores == nil {
			o.SubScores = map[string]string{}
		}
		o.SubScores[name] = sc.QualifierKey
	}
	return o
}

// --- garmin_hrv ---

// HRVOut is one night of HRV.
type HRVOut struct {
	Date           string `json:"date"`
	LastNightAvg   *int   `json:"last_night_avg_ms,omitempty"`
	LastNight5MinH *int   `json:"last_night_5min_high_ms,omitempty"`
	WeeklyAvg      *int   `json:"weekly_avg_ms,omitempty"`
	BaselineLow    *int   `json:"baseline_low_ms,omitempty"`
	BaselineHigh   *int   `json:"baseline_high_ms,omitempty"`
	Status         string `json:"status,omitempty"`
}

// HRVListOut wraps nights.
type HRVListOut struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Nights []HRVOut `json:"nights"`
}

func (h *handlers) hrv(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, HRVListOut, error) {
	from, to, err := h.resolve(in, 14, 366)
	if err != nil {
		return nil, HRVListOut{}, err
	}
	m, err := h.svc.HRV(ctx, from, to)
	if err != nil {
		return nil, HRVListOut{}, err
	}
	out := HRVListOut{From: from.String(), To: to.String(), Nights: []HRVOut{}}
	for _, k := range sortedKeys(m) {
		out.Nights = append(out.Nights, hrvOut(m[k]))
	}
	return nil, out, nil
}

func hrvOut(v garmin.HRVSummary) HRVOut {
	o := HRVOut{
		Date:           v.CalendarDate,
		LastNightAvg:   v.LastNightAvg,
		LastNight5MinH: v.LastNight5MinHigh,
		WeeklyAvg:      v.WeeklyAvg,
		Status:         v.Status,
	}
	if v.Baseline != nil {
		o.BaselineLow, o.BaselineHigh = v.Baseline.BalancedLow, v.Baseline.BalancedUpper
	}
	return o
}

// --- garmin_body_battery ---

// BodyBatteryInput adds the timeline switch to a range.
type BodyBatteryInput struct {
	RangeInput
	IncludeTimeline bool `json:"include_timeline,omitempty" jsonschema:"Include the intraday level series (adds roughly 1-3 KB per day)."`
}

// BBEvent is a Body Battery change event.
type BBEvent struct {
	Type        string `json:"type"`
	Start       string `json:"start_local,omitempty"`
	DurationMin *int   `json:"duration_min,omitempty"`
	Impact      *int   `json:"impact,omitempty"`
}

// BBPoint is one timeline sample.
type BBPoint struct {
	Time  string `json:"time"`
	Level int    `json:"level"`
}

// BBDayOut is one day of Body Battery.
type BBDayOut struct {
	Date     string    `json:"date"`
	Charged  *int      `json:"charged,omitempty"`
	Drained  *int      `json:"drained,omitempty"`
	Min      *int      `json:"min,omitempty"`
	Max      *int      `json:"max,omitempty"`
	Latest   *int      `json:"latest,omitempty"`
	Feedback string    `json:"feedback,omitempty"`
	Events   []BBEvent `json:"events,omitempty"`
	Timeline []BBPoint `json:"timeline,omitempty"`
}

// BBListOut wraps days.
type BBListOut struct {
	From string     `json:"from"`
	To   string     `json:"to"`
	Days []BBDayOut `json:"days"`
}

func (h *handlers) bodyBattery(ctx context.Context, _ *mcp.CallToolRequest, in BodyBatteryInput) (*mcp.CallToolResult, BBListOut, error) {
	from, to, err := h.resolve(in.RangeInput, 3, 31)
	if err != nil {
		return nil, BBListOut{}, err
	}
	m, err := h.svc.BodyBattery(ctx, from, to)
	if err != nil {
		return nil, BBListOut{}, err
	}
	out := BBListOut{From: from.String(), To: to.String(), Days: []BBDayOut{}}
	for _, k := range sortedKeys(m) {
		out.Days = append(out.Days, bbOut(m[k], h.svc.Location(), in.IncludeTimeline))
	}
	return nil, out, nil
}

func bbOut(r garmin.BodyBatteryReport, loc *time.Location, timeline bool) BBDayOut {
	o := BBDayOut{Date: r.Date, Charged: r.Charged, Drained: r.Drained}
	for _, v := range r.Values {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		lvl := int(*v[1])
		if o.Min == nil || lvl < *o.Min {
			o.Min = new(lvl)
		}
		if o.Max == nil || lvl > *o.Max {
			o.Max = new(lvl)
		}
		o.Latest = new(lvl)
		if timeline {
			t := time.UnixMilli(int64(*v[0])).In(loc).Format("15:04")
			o.Timeline = append(o.Timeline, BBPoint{Time: t, Level: lvl})
		}
	}
	if fb := r.EndOfDayFeedb; fb != nil && fb.FeedbackLongType != "" {
		o.Feedback = fb.FeedbackLongType
	} else if fb := r.Feedback; fb != nil {
		o.Feedback = fb.FeedbackLongType
	}
	for _, e := range r.ActivityEvt {
		ev := BBEvent{Type: e.EventType, DurationMin: msToMin(e.DurationInMillis), Impact: e.BodyBatteryImpact}
		if t, err := time.Parse("2006-01-02T15:04:05.0", e.EventStartTimeGmt); err == nil {
			if e.TimezoneOffset != nil {
				t = t.Add(time.Duration(*e.TimezoneOffset) * time.Millisecond)
			}
			ev.Start = t.Format("2006-01-02 15:04")
		}
		o.Events = append(o.Events, ev)
	}
	return o
}

// --- garmin_heart_rate ---

// HeartRateInput selects a day and aggregation interval.
type HeartRateInput struct {
	Date            string `json:"date,omitempty" jsonschema:"Day, YYYY-MM-DD. Default: today."`
	IntervalMinutes int    `json:"interval_minutes,omitempty" jsonschema:"Aggregation interval in minutes: 5, 10, 15, 20, 30, 60, 120, 180 or 240. Default 60."`
}

// HRBucket is one aggregation interval.
type HRBucket struct {
	Start string `json:"start"`
	Avg   int    `json:"avg"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
}

// HeartRateOut is one day of heart rate.
type HeartRateOut struct {
	Date         string     `json:"date"`
	RestingHR    *int       `json:"resting_hr_bpm,omitempty"`
	RestingHR7d  *int       `json:"resting_hr_7d_avg_bpm,omitempty"`
	MinHR        *int       `json:"min_hr_bpm,omitempty"`
	MaxHR        *int       `json:"max_hr_bpm,omitempty"`
	IntervalMin  int        `json:"interval_min"`
	Series       []HRBucket `json:"series"`
	NoDataReason string     `json:"no_data_reason,omitempty"`
}

func (h *handlers) heartRate(ctx context.Context, _ *mcp.CallToolRequest, in HeartRateInput) (*mcp.CallToolResult, HeartRateOut, error) {
	day, err := h.parseDay(in.Date)
	if err != nil {
		return nil, HeartRateOut{}, err
	}
	iv := in.IntervalMinutes
	if iv == 0 {
		iv = 60
	}
	if iv < 5 || iv > 240 || 1440%iv != 0 {
		return nil, HeartRateOut{}, fmt.Errorf("interval_minutes must divide a day evenly and be 5-240 (5, 10, 15, 20, 30, 60, 120, 180, 240), got %d", iv)
	}
	d, err := h.svc.HeartRate(ctx, day)
	if err != nil {
		return nil, HeartRateOut{}, err
	}
	out := HeartRateOut{Date: day.String(), IntervalMin: iv, Series: []HRBucket{}}
	if d == nil {
		out.NoDataReason = "Garmin has no heart rate for this day (watch not worn or not synced yet)"
		return nil, out, nil
	}
	out.RestingHR, out.RestingHR7d, out.MinHR, out.MaxHR = d.RestingHeartRate, d.LastSevenDaysAvgRHR, d.MinHeartRate, d.MaxHeartRate

	// Bucket by local wall-clock minute since midnight, so buckets and labels
	// stay aligned on DST days (a repeated hour folds into the same bucket).
	loc := h.svc.Location()
	type acc struct{ sum, n, min, max int }
	buckets := map[int]*acc{}
	for _, v := range d.HeartRateValues {
		if len(v) < 2 || v[0] == nil || v[1] == nil || *v[1] <= 0 {
			continue
		}
		t := time.UnixMilli(int64(*v[0])).In(loc)
		if !civil.Of(t).Equal(day) {
			continue
		}
		bpm := int(*v[1])
		key := (t.Hour()*60 + t.Minute()) / iv
		b, ok := buckets[key]
		if !ok {
			b = &acc{min: bpm, max: bpm}
			buckets[key] = b
		}
		b.sum += bpm
		b.n++
		b.min, b.max = min(b.min, bpm), max(b.max, bpm)
	}
	keys := make([]int, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b := buckets[k]
		m := k * iv
		out.Series = append(out.Series, HRBucket{
			Start: fmt.Sprintf("%02d:%02d", m/60, m%60),
			Avg:   (b.sum + b.n/2) / b.n, Min: b.min, Max: b.max,
		})
	}
	return nil, out, nil
}

// --- garmin_weight ---

// WeightOut is one weigh-in.
type WeightOut struct {
	Date         string   `json:"date"`
	WeightKg     *float64 `json:"weight_kg,omitempty"`
	BMI          *float64 `json:"bmi,omitempty"`
	BodyFatPct   *float64 `json:"body_fat_pct,omitempty"`
	MuscleMassKg *float64 `json:"muscle_mass_kg,omitempty"`
	BodyWaterPct *float64 `json:"body_water_pct,omitempty"`
	Source       string   `json:"source,omitempty"`
}

// WeightListOut wraps weigh-ins.
type WeightListOut struct {
	From    string      `json:"from"`
	To      string      `json:"to"`
	Entries []WeightOut `json:"entries"`
}

func (h *handlers) weight(ctx context.Context, _ *mcp.CallToolRequest, in RangeInput) (*mcp.CallToolResult, WeightListOut, error) {
	from, to, err := h.resolve(in, 90, 731)
	if err != nil {
		return nil, WeightListOut{}, err
	}
	m, err := h.svc.Weight(ctx, from, to)
	if err != nil {
		return nil, WeightListOut{}, err
	}
	out := WeightListOut{From: from.String(), To: to.String(), Entries: []WeightOut{}}
	for _, k := range sortedKeys(m) {
		out.Entries = append(out.Entries, weightOut(k, m[k]))
	}
	return nil, out, nil
}

func weightOut(date string, w garmin.WeightSample) WeightOut {
	return WeightOut{
		Date:         date,
		WeightKg:     scaled(w.Weight, 0.001, 1),
		BMI:          rp(w.BMI, 1),
		BodyFatPct:   rp(w.BodyFat, 1),
		MuscleMassKg: scaled(w.MuscleMass, 0.001, 1),
		BodyWaterPct: rp(w.BodyWater, 1),
		Source:       strings.ToLower(w.SourceType),
	}
}
