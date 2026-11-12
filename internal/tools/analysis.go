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
	from, to, err := h.resolve(RangeInput{EndDate: in.EndDate, Days: days}, 0, 731)
	if err != nil {
		return nil, TrendsOut{}, err
	}
	d := trendData{period: period, byStart: map[string]*trendAcc{}}
	err = fetchAll(
		func() (err error) { d.sleep, err = h.svc.Sleep(ctx, from, to); return },
		func() (err error) { d.hrv, err = h.svc.HRV(ctx, from, to); return },
		func() (err error) { d.sums, err = h.svc.DailySummaries(ctx, from, to); return },
		func() (err error) { d.weight, err = h.svc.Weight(ctx, from, to); return },
		func() (err error) { d.acts, err = h.svc.Activities(ctx, from, to, "", "", maxTrendActivities); return },
	)
	if err != nil {
		return nil, TrendsOut{}, err
	}

	for _, day := range civil.Range(from, to) {
		d.addDay(day)
	}
	for _, act := range d.acts {
		if day, err := civil.Parse(firstN(act.StartTimeLocal, 10)); err == nil && !day.Before(from) && !day.After(to) {
			d.bucket(day).addActivity(act)
		}
	}
	out := TrendsOut{From: from.String(), To: to.String(), Period: period, Buckets: []TrendBucket{},
		Note: "Averages ignore days without data. Intensity minutes count vigorous minutes twice (WHO convention, as Garmin does); compare intensity_minutes_per_7d with the WHO target of 150. The first and last buckets may be partial: check calendar_days and days_with_data."}
	if len(d.acts) >= maxTrendActivities {
		out.Warning = fmt.Sprintf("more than %d activities in this period: the oldest buckets undercount workouts and load; use a shorter period", maxTrendActivities)
	}
	for _, k := range d.order {
		out.Buckets = append(out.Buckets, d.byStart[k].result())
	}
	return nil, out, nil
}

// trendData holds the fetched data and the buckets in chronological order.
type trendData struct {
	period  string
	sleep   map[string]garmin.DailySleep
	hrv     map[string]garmin.HRVSummary
	sums    map[string]garmin.DailySummary
	weight  map[string]garmin.WeightSample
	acts    []garmin.Activity
	byStart map[string]*trendAcc
	order   []string
}

type trendAcc struct {
	b                                             TrendBucket
	rhr, hrv, sleepH, score, steps, stress, bb, w mean
}

// bucket returns the week (Monday-based) or month bucket containing day.
func (d *trendData) bucket(day civil.Date) *trendAcc {
	start := day.AddDays(-((int(day.Weekday()) + 6) % 7))
	if d.period == "month" {
		start = day.Month()
	}
	k := start.String()
	a, ok := d.byStart[k]
	if !ok {
		a = &trendAcc{b: TrendBucket{Start: k}}
		d.byStart[k] = a
		d.order = append(d.order, k)
	}
	return a
}

func (d *trendData) addDay(day civil.Date) {
	a, k := d.bucket(day), day.String()
	a.b.CalendarDays++
	if s, ok := d.sums[k]; ok {
		a.b.DaysWithData++
		a.rhr.addInt(s.HeartRate.RestingValue)
		a.steps.addInt(s.Movement.Steps.Value)
		a.stress.addInt(s.Stress.AvgLevel)
		a.bb.addInt(s.BodyBattery.MaxValue)
		a.b.IntensityMin += deref(s.IntensityMinutes.Moderate) + 2*deref(s.IntensityMinutes.Vigorous)
	}
	so := sleepOut(d.sleep[k])
	a.sleepH.add(so.SleepHours)
	a.score.add(so.Score)
	a.hrv.addInt(d.hrv[k].LastNightAvg)
	a.w.add(scaled(d.weight[k].Weight, 0.001, 2))
}

func (a *trendAcc) addActivity(act garmin.Activity) {
	a.b.Workouts++
	if act.Duration != nil {
		a.b.WorkoutMin += int(math.Round(*act.Duration / 60))
	}
	if act.ActivityTrainingLoad != nil {
		a.b.TrainingLoad += int(math.Round(*act.ActivityTrainingLoad))
	}
}

func (a *trendAcc) result() TrendBucket {
	b := a.b
	b.RestingHR, b.HRV = a.rhr.value(1), a.hrv.value(1)
	b.SleepHours, b.SleepScore = a.sleepH.value(2), a.score.value(0)
	b.Steps, b.Stress, b.BodyBatteryMax = a.steps.value(0), a.stress.value(0), a.bb.value(0)
	b.WeightKg = a.w.value(1)
	b.IntensityPer7d = int(math.Round(float64(b.IntensityMin) * 7 / float64(b.CalendarDays)))
	return b
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
