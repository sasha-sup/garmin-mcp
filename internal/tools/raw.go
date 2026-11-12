package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

const rawLimit = 100 << 10

// rawEndpoint is one allow-listed GET endpoint. Path parameters are filled
// only from validated values, so the tool cannot reach arbitrary URLs.
type rawEndpoint struct {
	path      string   // with {date}, {start}, {end}, {id}
	query     []string // query keys passed through from the input
	sensitive bool     // reproductive health and nutrition: only with Options.AllowSensitive
}

var rawEndpoints = map[string]rawEndpoint{
	"daily_summary_v1":    {path: "/usersummary-service/usersummary/daily", query: []string{"calendarDate"}},
	"daily_summaries_v2":  {path: "/usersummary-service/v2/dailysummaries", query: []string{"startDate", "endDate"}},
	"sleep_day":           {path: "/sleep-service/sleep/dailySleep", query: []string{"date"}},
	"sleep_range":         {path: "/sleep-service/sleep/dailySleepSummariesByDate", query: []string{"startDate", "endDate"}},
	"hrv_day":             {path: "/hrv-service/hrv/{date}"},
	"hrv_range":           {path: "/hrv-service/hrv/daily/{start}/{end}"},
	"body_battery":        {path: "/wellness-service/wellness/bodyBattery/reports/daily", query: []string{"startDate", "endDate", "chunks"}},
	"heart_rate_day":      {path: "/wellness-service/wellness/dailyHeartRate", query: []string{"date"}},
	"spo2_day":            {path: "/wellness-service/wellness/daily/spo2acclimation/{date}"},
	"stress_daily":        {path: "/usersummary-service/stats/stress/daily/{start}/{end}"},
	"steps_daily":         {path: "/usersummary-service/stats/steps/daily/{start}/{end}"},
	"hydration_daily":     {path: "/usersummary-service/stats/hydration/daily/{start}/{end}"},
	"wellness_activities": {path: "/wellnessactivity-service/activity/summary/range/{start}/{end}"},
	"activity_search": {path: "/activitylist-service/activities/search/activities", query: []string{
		"start", "limit", "startDate", "endDate", "activityType", "activitySubType", "eventType", "search",
		"sortBy", "sortOrder", "minDistance", "maxDistance", "minDuration", "maxDuration", "favorite", "excludeChildren"}},
	"activity":                {path: "/activity-service/activity/{id}"},
	"activity_workouts":       {path: "/activity-service/activity/{id}/workouts"},
	"hr_zones":                {path: "/biometric-service/heartRateZones/v2", query: []string{"generateDefaults"}},
	"fitness_age":             {path: "/fitnessage-service/fitnessage/v2/{date}", query: []string{"fullHistoryScan"}},
	"weight_all":              {path: "/weight-service/weight/all/{start}/{end}"},
	"weight_range":            {path: "/weight-service/weight/range/{start}/{end}", query: []string{"includeAll"}},
	"weight_goal":             {path: "/goal-service/goal/user/effective/weightgoal/{start}/{end}"},
	"nutrition_day":           {path: "/nutrition-service/food/logs/{date}", query: []string{"includeGoals", "includeNutrition", "mealId"}, sensitive: true},
	"menstrual_cycle_summary": {path: "/periodichealth-service/menstrualcycle/summary/{date}", sensitive: true},
	"menstrual_cycle_day":     {path: "/periodichealth-service/menstrualcycle/dayview/{date}", query: []string{"todayDate", "reducedResponse", "includeAnalysis"}, sensitive: true},
	"profile_settings":        {path: "/userprofile-service/userprofile/settings"},
}

// RawEndpointNames lists the endpoint names enabled for the given options, sorted.
func RawEndpointNames(allowSensitive bool) []string {
	names := make([]string, 0, len(rawEndpoints))
	for k, ep := range rawEndpoints {
		if ep.sensitive && !allowSensitive {
			continue
		}
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// RawInput selects an endpoint and its parameters.
type RawInput struct {
	Endpoint   string            `json:"endpoint" jsonschema:"Endpoint name (see enum)."`
	Date       string            `json:"date,omitempty" jsonschema:"YYYY-MM-DD for endpoints with {date}."`
	StartDate  string            `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD for endpoints with {start}."`
	EndDate    string            `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD for endpoints with {end}."`
	ActivityID int64             `json:"activity_id,omitempty" jsonschema:"Activity ID for endpoints with {id}."`
	Query      map[string]string `json:"query,omitempty" jsonschema:"Query parameters, e.g. {\"calendarDate\":\"2026-11-11\"}. Only keys documented for the endpoint are accepted."`
}

var queryValueRE = regexp.MustCompile(`^[\p{L}\p{N} _.:,-]{0,100}$`)

func (h *handlers) apiGet(ctx context.Context, _ *mcp.CallToolRequest, in RawInput) (*mcp.CallToolResult, any, error) {
	ep, ok := rawEndpoints[in.Endpoint]
	if !ok {
		return nil, nil, fmt.Errorf("unknown endpoint %q; allowed: %s", in.Endpoint, strings.Join(RawEndpointNames(h.opts.AllowSensitive), ", "))
	}
	if ep.sensitive && !h.opts.AllowSensitive {
		return nil, nil, fmt.Errorf("endpoint %s returns reproductive-health or nutrition data and is disabled; the user can enable it with GARMIN_ALLOW_SENSITIVE=1", in.Endpoint)
	}
	path, err := rawPath(ep, in)
	if err != nil {
		return nil, nil, err
	}
	q, err := rawQuery(ep, in)
	if err != nil {
		return nil, nil, err
	}
	body, err := h.svc.Client().Get(ctx, path, q)
	if errors.Is(err, garmin.ErrNoData) {
		return textResult("Garmin returned no data (HTTP 204) for this request."), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return textResult(truncateRaw(body)), nil, nil
}

// rawPath fills the endpoint's path placeholders from validated input only.
func rawPath(ep rawEndpoint, in RawInput) (string, error) {
	path := ep.path
	for _, p := range []struct{ placeholder, value, name string }{
		{"{date}", in.Date, "date"},
		{"{start}", in.StartDate, "start_date"},
		{"{end}", in.EndDate, "end_date"},
	} {
		if !strings.Contains(path, p.placeholder) {
			continue
		}
		d, err := civil.Parse(p.value)
		if err != nil {
			return "", fmt.Errorf("endpoint %s needs %s: %w", in.Endpoint, p.name, err)
		}
		path = strings.Replace(path, p.placeholder, d.String(), 1)
	}
	if strings.Contains(path, "{id}") {
		if in.ActivityID <= 0 {
			return "", fmt.Errorf("endpoint %s needs activity_id", in.Endpoint)
		}
		path = strings.Replace(path, "{id}", strconv.FormatInt(in.ActivityID, 10), 1)
	}
	return path, nil
}

func rawQuery(ep rawEndpoint, in RawInput) (url.Values, error) {
	q := url.Values{}
	for k, v := range in.Query {
		if !slices.Contains(ep.query, k) {
			return nil, fmt.Errorf("query key %q is not allowed for %s; allowed: %v", k, in.Endpoint, ep.query)
		}
		if !queryValueRE.MatchString(v) {
			return nil, fmt.Errorf("query value for %q contains unsupported characters", k)
		}
		q.Set(k, v)
	}
	return q, nil
}

// truncateRaw cuts body at rawLimit on a UTF-8 boundary and says so.
func truncateRaw(body []byte) string {
	if len(body) <= rawLimit {
		return string(body)
	}
	cut := rawLimit
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return string(body[:cut]) + fmt.Sprintf("\n…[truncated: response is %d bytes, showing the first %d; narrow the date range]", len(body), cut)
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
