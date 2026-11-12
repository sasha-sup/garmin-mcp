package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// --- garmin_trends ---

const maxTrendActivities = 3000

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
	CalendarDays   int      `json:"calendar_days"`
	IntensityMin   int      `json:"intensity_minutes_total"`
	IntensityPer7d int      `json:"intensity_minutes_per_7d"`
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
	Warning string        `json:"warning,omitempty"`
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
	wg.Go(func() { acts, eAct = h.svc.Activities(ctx, from, to, "", "", maxTrendActivities) })
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
		a.b.CalendarDays++
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
		Note: "Averages ignore days without data. Intensity minutes count vigorous minutes twice (WHO convention, as Garmin does); compare intensity_minutes_per_7d with the WHO target of 150. The first and last buckets may be partial: check calendar_days and days_with_data."}
	if len(acts) >= maxTrendActivities {
		out.Warning = fmt.Sprintf("more than %d activities in this period: the oldest buckets undercount workouts and load; use a shorter period", maxTrendActivities)
	}
	for _, k := range order {
		a := accs[k]
		a.b.RestingHR, a.b.HRV = a.rhr.value(1), a.hrv.value(1)
		a.b.SleepHours, a.b.SleepScore = a.sleepH.value(2), a.score.value(0)
		a.b.Steps, a.b.Stress, a.b.BodyBatteryMax = a.steps.value(0), a.stress.value(0), a.bb.value(0)
		a.b.WeightKg = a.w.value(1)
		if a.b.CalendarDays > 0 {
			a.b.IntensityPer7d = int(math.Round(float64(a.b.IntensityMin) * 7 / float64(a.b.CalendarDays)))
		}
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
