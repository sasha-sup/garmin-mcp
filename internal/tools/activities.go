package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// ActivitiesInput filters the activity list.
type ActivitiesInput struct {
	RangeInput
	ActivityType string `json:"activity_type,omitempty" jsonschema:"Garmin parent type key, e.g. running, cycling, swimming, fitness_equipment, walking, hiking."`
	Search       string `json:"search,omitempty" jsonschema:"Text to match in activity names."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum activities to return, 1-100. Default 20."`
}

// ActivityOut is a compact workout.
type ActivityOut struct {
	ID              int64    `json:"activity_id"`
	Start           string   `json:"start_local"`
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	DurationMin     *float64 `json:"duration_min,omitempty"`
	MovingMin       *float64 `json:"moving_min,omitempty"`
	DistanceKm      *float64 `json:"distance_km,omitempty"`
	Pace            string   `json:"pace_min_per_km,omitempty"`
	SpeedKmh        *float64 `json:"speed_kmh,omitempty"`
	AvgHR           *float64 `json:"avg_hr_bpm,omitempty"`
	MaxHR           *float64 `json:"max_hr_bpm,omitempty"`
	HRZonesMin      []int    `json:"hr_zones_min,omitempty"`
	Calories        *float64 `json:"calories_kcal,omitempty"`
	ElevationGainM  *float64 `json:"elevation_gain_m,omitempty"`
	AvgPowerW       *float64 `json:"avg_power_w,omitempty"`
	CadenceSpm      *float64 `json:"cadence_spm,omitempty"`
	TrainingLoad    *float64 `json:"training_load,omitempty"`
	AerobicTE       *float64 `json:"aerobic_te,omitempty"`
	AnaerobicTE     *float64 `json:"anaerobic_te,omitempty"`
	TELabel         string   `json:"training_effect_label,omitempty"`
	VO2Max          *float64 `json:"vo2max,omitempty"`
	BodyBatteryDiff *int     `json:"body_battery_impact,omitempty"`
	Sets            *int     `json:"sets,omitempty"`
	Reps            *int     `json:"reps,omitempty"`
	PersonalRecord  bool     `json:"personal_record,omitempty"`
	Manual          bool     `json:"manual,omitempty"`
}

// ActivitiesOut wraps the list.
type ActivitiesOut struct {
	From       string        `json:"from"`
	To         string        `json:"to"`
	Count      int           `json:"count"`
	Truncated  bool          `json:"truncated,omitempty"`
	Activities []ActivityOut `json:"activities"`
}

func (h *handlers) activities(ctx context.Context, _ *mcp.CallToolRequest, in ActivitiesInput) (*mcp.CallToolResult, ActivitiesOut, error) {
	from, to, err := h.resolve(in.RangeInput, 30, 3660)
	if err != nil {
		return nil, ActivitiesOut{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, ActivitiesOut{}, fmt.Errorf("limit must be between 1 and 100, got %d", limit)
	}
	// Ask for one extra to know whether the list was cut.
	list, err := h.svc.Activities(ctx, from, to, strings.TrimSpace(in.ActivityType), strings.TrimSpace(in.Search), limit+1)
	if err != nil {
		return nil, ActivitiesOut{}, err
	}
	out := ActivitiesOut{From: from.String(), To: to.String(), Activities: []ActivityOut{}}
	if len(list) > limit {
		list, out.Truncated = list[:limit], true
	}
	for _, a := range list {
		out.Activities = append(out.Activities, activityOut(a))
	}
	out.Count = len(out.Activities)
	return nil, out, nil
}

func activityOut(a garmin.Activity) ActivityOut {
	o := ActivityOut{
		ID:              a.ActivityID,
		Start:           a.StartTimeLocal,
		Name:            a.ActivityName,
		Type:            a.ActivityType.TypeKey,
		DurationMin:     scaled(a.Duration, 1.0/60, 1),
		MovingMin:       scaled(a.MovingDuration, 1.0/60, 1),
		AvgHR:           rp(a.AverageHR, 0),
		MaxHR:           rp(a.MaxHR, 0),
		Calories:        rp(a.Calories, 0),
		ElevationGainM:  rp(a.ElevationGain, 0),
		AvgPowerW:       rp(a.AvgPower, 0),
		CadenceSpm:      rp(a.AvgRunCadence, 0),
		TrainingLoad:    rp(a.ActivityTrainingLoad, 0),
		AerobicTE:       rp(a.AerobicTrainingEffect, 1),
		AnaerobicTE:     rp(a.AnaerobicTrainingEff, 1),
		TELabel:         a.TrainingEffectLabel,
		VO2Max:          rp(a.VO2MaxValue, 0),
		BodyBatteryDiff: a.DifferenceBodyBattery,
		Sets:            a.TotalSets,
		Reps:            a.TotalReps,
		PersonalRecord:  a.IsPR,
		Manual:          a.ManualActivity,
	}
	if a.Distance != nil && *a.Distance > 0 {
		o.DistanceKm = scaled(a.Distance, 0.001, 2)
		if a.AverageSpeed != nil && *a.AverageSpeed > 0 {
			if usesPace(o.Type) {
				o.Pace = pace(*a.AverageSpeed)
			} else {
				o.SpeedKmh = scaled(a.AverageSpeed, 3.6, 1)
			}
		}
	}
	zones := []*float64{a.HRTimeInZone1, a.HRTimeInZone2, a.HRTimeInZone3, a.HRTimeInZone4, a.HRTimeInZone5}
	if slices.ContainsFunc(zones, func(z *float64) bool { return z != nil }) {
		o.HRZonesMin = make([]int, len(zones))
		for i, z := range zones {
			if z != nil {
				o.HRZonesMin[i] = int(math.Round(*z / 60))
			}
		}
	}
	if o.Sets != nil && *o.Sets == 0 {
		o.Sets, o.Reps = nil, nil
	}
	return o
}

func usesPace(typeKey string) bool {
	for _, s := range []string{"run", "walk", "hik"} {
		if strings.Contains(typeKey, s) {
			return true
		}
	}
	return false
}

// pace formats metres per second as min:sec per km.
func pace(mps float64) string {
	sec := int(math.Round(1000 / mps))
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// ActivityInput selects one activity.
type ActivityInput struct {
	ActivityID int64 `json:"activity_id" jsonschema:"Activity ID from garmin_activities."`
}

// ActivityDetailOut is the full activity summary.
type ActivityDetailOut struct {
	ID      int64          `json:"activity_id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Event   string         `json:"event_type,omitempty"`
	Units   string         `json:"units"`
	Summary map[string]any `json:"summary"`
}

func (h *handlers) activity(ctx context.Context, _ *mcp.CallToolRequest, in ActivityInput) (*mcp.CallToolResult, ActivityDetailOut, error) {
	if in.ActivityID <= 0 {
		return nil, ActivityDetailOut{}, errors.New("activity_id must be a positive number")
	}
	d, err := h.svc.Activity(ctx, in.ActivityID)
	if err != nil {
		return nil, ActivityDetailOut{}, err
	}
	summary := d.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	return nil, ActivityDetailOut{
		ID:      d.ActivityID,
		Name:    d.ActivityName,
		Type:    d.ActivityType.TypeKey,
		Event:   d.EventType.TypeKey,
		Units:   "Garmin raw units: distance m, duration s, speed m/s, elevation m, temperature °C",
		Summary: summary,
	}, nil
}
