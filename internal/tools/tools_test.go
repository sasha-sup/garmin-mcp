package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/civil"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

// fakeGarmin serves synthetic data for every endpoint the tools use.
func fakeGarmin(t *testing.T) *httptest.Server {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	token := enc([]byte(`{}`)) + "." + enc(fmt.Appendf(nil, `{"exp":%d}`, time.Now().Add(time.Hour).Unix())) + ".c2ln"

	days := func(_ *http.Request, from, to string) []civil.Date {
		f, _ := civil.Parse(from)
		e, _ := civil.Parse(to)
		return civil.Range(f, e)
	}
	join := func(items []string) string { return "[" + strings.Join(items, ",") + "]" }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.Query()
		if p == "/personalapitoken-service/personal-api/personal-api-key/token" {
			fmt.Fprint(w, token)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var items []string
		switch {
		case p == "/usersummary-service/v2/dailysummaries":
			for i, d := range days(r, q.Get("startDate"), q.Get("endDate")) {
				items = append(items, fmt.Sprintf(`{"calendarDate":%q,"movement":{"steps":{"value":%d,"goal":8000,"distanceInMeters":6500}},
					"heartRate":{"restingValue":%d,"minValue":50,"maxValue":150},"stress":{"avgLevel":%d,"qualifier":"balanced"},
					"bodyBattery":{"minValue":10,"maxValue":90},"intensityMinutes":{"moderate":20,"vigorous":10},"calories":{"burnedTotal":2500}}`,
					d, 4000+i*500, 55+i%3, 25+i%30))
			}
			fmt.Fprintf(w, `{"data":%s}`, join(items))
		case p == "/sleep-service/sleep/dailySleepSummariesByDate":
			for i, d := range days(r, q.Get("startDate"), q.Get("endDate")) {
				start := time.Date(2026, 1, 1, 23, i%4*15, 0, 0, time.UTC).UnixMilli()
				items = append(items, fmt.Sprintf(`{"calendarDate":%q,"sleepTimeSeconds":%d,"deepSleepSeconds":5400,"lightSleepSeconds":14400,
					"remSleepSeconds":6000,"awakeSleepSeconds":600,"sleepStartTimestampLocal":%d,"sleepEndTimestampLocal":%d,"awakeCount":1,
					"avgHeartRate":58,"lowestSpO2Value":91,"sleepNeed":{"baseline":480,"actual":500},
					"sleepScores":{"overall":{"value":%d,"qualifierKey":"GOOD"},"stress":{"qualifierKey":"FAIR"}}}`,
					d, 24000+i*120, start, start+8*3600_000, 60+i%25))
			}
			fmt.Fprint(w, join(items))
		case strings.HasPrefix(p, "/hrv-service/hrv/daily/"):
			parts := strings.Split(p, "/")
			for i, d := range days(r, parts[4], parts[5]) {
				items = append(items, fmt.Sprintf(`{"calendarDate":%q,"lastNightAvg":%d,"weeklyAvg":45,"status":"BALANCED",
					"baseline":{"balancedLow":38,"balancedUpper":55}}`, d, 40+i%10))
			}
			fmt.Fprintf(w, `{"hrvSummaries":%s}`, join(items))
		case p == "/wellness-service/wellness/bodyBattery/reports/daily":
			for _, d := range days(r, q.Get("startDate"), q.Get("endDate")) {
				items = append(items, fmt.Sprintf(`{"date":%q,"charged":60,"drained":55,"bodyBatteryValuesArray":[[1767225600000,20],[1767254400000,80]],
					"bodyBatteryActivityEvent":[{"eventType":"SLEEP","eventStartTimeGmt":"2026-01-01T20:00:00.0","timezoneOffset":14400000,
					"durationInMilliseconds":28800000,"bodyBatteryImpact":55}]}`, d))
			}
			fmt.Fprint(w, join(items))
		case p == "/wellness-service/wellness/dailyHeartRate":
			fmt.Fprintf(w, `{"calendarDate":%q,"restingHeartRate":55,"heartRateValues":[[1767225600000,60],[1767225720000,70],[1767229200000,null]]}`, q.Get("date"))
		case p == "/activitylist-service/activities/search/activities":
			if q.Get("start") != "0" {
				fmt.Fprint(w, "[]")
				return
			}
			end, _ := civil.Parse(q.Get("endDate"))
			fmt.Fprintf(w, `[{"activityId":11,"activityName":"Evening run","startTimeLocal":"%s 20:30:00","activityType":{"typeKey":"running"},
				"distance":10000,"duration":3000,"averageSpeed":3.3333,"activityTrainingLoad":160,"hrTimeInZone_2":1200},
				{"activityId":12,"activityName":"Ride","startTimeLocal":"%s 08:00:00","activityType":{"typeKey":"cycling"},
				"distance":30000,"duration":3600,"averageSpeed":8.3333,"activityTrainingLoad":90}]`, end, end.AddDays(-3))
		case p == "/activity-service/activity/11":
			fmt.Fprint(w, `{"activityId":11,"activityName":"Evening run","activityTypeDTO":{"typeKey":"running"},"summaryDTO":{"distance":10000}}`)
		case strings.HasPrefix(p, "/weight-service/weight/range/"):
			parts := strings.Split(p, "/")
			fmt.Fprintf(w, `{"dailyWeightSummaries":[{"summaryDate":%q,"latestWeight":{"weight":80500,"bodyFat":18.2}}]}`, parts[5])
		case p == "/userprofile-service/userprofile/settings":
			fmt.Fprint(w, `{"timeZone":"UTC","measurementSystem":"metric"}`)
		case p == "/biometric-service/heartRateZones/v2":
			fmt.Fprint(w, `[{"sport":"DEFAULT","zone1Floor":100,"maxHeartRateUsed":190}]`)
		case strings.HasPrefix(p, "/fitnessage-service/fitnessage/v2/"):
			fmt.Fprint(w, `{"chronologicalAge":35,"fitnessAge":30.5}`)
		case strings.HasPrefix(p, "/wellness-service/wellness/daily/spo2acclimation/"):
			fmt.Fprint(w, `{"averageSpO2":96}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	g := fakeGarmin(t)
	client, err := garmin.New("k", garmin.WithBaseURL(g.URL))
	if err != nil {
		t.Fatal(err)
	}
	svc := garmin.NewService(client, nil, time.UTC, nil)
	srv := mcp.NewServer(&mcp.Implementation{Name: "garmin-mcp", Version: "test"}, &mcp.ServerOptions{Instructions: Instructions})
	Register(srv, svc, Options{})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestEveryToolSucceeds(t *testing.T) {
	cs := connect(t)
	cases := map[string]map[string]any{
		"garmin_daily_summary":   {"days": 10},
		"garmin_sleep":           {"days": 40},
		"garmin_sleep_analysis":  {"days": 28},
		"garmin_hrv":             {},
		"garmin_body_battery":    {"days": 2, "include_timeline": true},
		"garmin_heart_rate":      {"interval_minutes": 30},
		"garmin_activities":      {"limit": 5},
		"garmin_activity":        {"activity_id": 11},
		"garmin_recovery_status": {},
		"garmin_trends":          {"days": 60, "period": "month"},
		"garmin_profile":         {},
		"garmin_weight":          {"days": 30},
		"garmin_api_get":         {"endpoint": "spo2_day", "date": "2026-01-01"},
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != len(cases) {
		t.Errorf("server has %d tools, test covers %d", len(tools.Tools), len(cases))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: not marked read-only", tool.Name)
		}
		args, ok := cases[tool.Name]
		if !ok {
			t.Errorf("%s: no test case", tool.Name)
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			text, isErr := call(t, cs, tool.Name, args)
			if isErr {
				t.Fatalf("tool error: %s", text)
			}
			if !json.Valid([]byte(text)) {
				t.Fatalf("output is not JSON: %.200s", text)
			}
		})
	}
}

func TestSleepAnalysisContent(t *testing.T) {
	cs := connect(t)
	text, isErr := call(t, cs, "garmin_sleep_analysis", map[string]any{"days": 28})
	if isErr {
		t.Fatal(text)
	}
	var out SleepAnalysisOut
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Nights != 28 || out.Averages.SleepHours == nil || out.Regularity.MedianOnset == "" || len(out.Worst) != 3 {
		t.Fatalf("unexpected analysis: %s", text)
	}
	// Need is the 480 min baseline, not the adjusted 500 min "actual".
	if out.Averages.SleepNeedHours == nil || *out.Averages.SleepNeedHours != 8 {
		t.Errorf("sleep need = %v, want 8 (baseline)", out.Averages.SleepNeedHours)
	}
	if out.Averages.WeeklyShortfallH == nil || *out.Averages.WeeklyShortfallH <= 0 {
		t.Errorf("weekly shortfall missing: need 8 h, slept ~7.1 h")
	}
	if out.StageShare.Nights != 28 {
		t.Errorf("stage nights = %d", out.StageShare.Nights)
	}
	for _, f := range out.Factors {
		if (f.NightsWith < minFactorNights || f.NightsWithout < minFactorNights) && f.ScoreDiff != nil {
			t.Errorf("factor %q shows a difference with %d/%d nights", f.Factor, f.NightsWith, f.NightsWithout)
		}
	}
	for _, w := range out.Worst {
		for _, b := range out.Best {
			if w.Date == b.Date {
				t.Errorf("night %s is both worst and best", w.Date)
			}
		}
	}
}

func TestRecoveryStatusContent(t *testing.T) {
	cs := connect(t)
	text, isErr := call(t, cs, "garmin_recovery_status", nil)
	if isErr {
		t.Fatal(text)
	}
	var out RecoveryOut
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if out.HRV == nil || out.RestingHR.Value == nil || out.Sleep == nil || out.Load == nil || len(out.Signals) == 0 {
		t.Fatalf("incomplete snapshot: %s", text)
	}
	if out.RestingHR.N != baselineDays || out.RestingHR.Z == nil {
		t.Errorf("resting HR baseline n=%d z=%v, want n=%d and a z-score", out.RestingHR.N, out.RestingHR.Z, baselineDays)
	}
}

func TestActivityFormatting(t *testing.T) {
	cs := connect(t)
	text, _ := call(t, cs, "garmin_activities", nil)
	for _, want := range []string{`"pace_min_per_km":"5:00"`, `"speed_kmh":30`, `"hr_zones_min":[0,20,0,0,0]`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
}

func TestInputValidation(t *testing.T) {
	cs := connect(t)
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"garmin_sleep", map[string]any{"start_date": "2026-13-01"}},
		{"garmin_sleep", map[string]any{"days": 1000}},
		{"garmin_sleep", map[string]any{"start_date": "2026-01-10", "end_date": "2026-01-01"}},
		{"garmin_body_battery", map[string]any{"days": 40}},
		{"garmin_heart_rate", map[string]any{"interval_minutes": 1}},
		{"garmin_heart_rate", map[string]any{"interval_minutes": 7}},
		{"garmin_api_get", map[string]any{"endpoint": "menstrual_cycle_summary", "date": "2026-01-01"}},
		{"garmin_activities", map[string]any{"limit": 500}},
		{"garmin_activity", map[string]any{"activity_id": -1}},
		{"garmin_trends", map[string]any{"period": "year"}},
		{"garmin_api_get", map[string]any{"endpoint": "../../admin"}},
		{"garmin_api_get", map[string]any{"endpoint": "hrv_day", "date": "../x"}},
		{"garmin_api_get", map[string]any{"endpoint": "sleep_day", "query": map[string]any{"evil": "1"}}},
		{"garmin_api_get", map[string]any{"endpoint": "sleep_day", "query": map[string]any{"date": "2026-01-01&x=1"}}},
	}
	for _, b := range bad {
		if text, isErr := call(t, cs, b.tool, b.args); !isErr {
			t.Errorf("%s %v: want error, got %.120s", b.tool, b.args, text)
		}
	}
}

func TestPrompts(t *testing.T) {
	cs := connect(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "sleep_review", Arguments: map[string]string{"days": "14"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, "last 14 days") {
		t.Fatalf("prompt = %s", text)
	}
	if _, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "sleep_review", Arguments: map[string]string{"days": "x"}}); err == nil {
		t.Fatal("want error for bad days")
	}
}

func TestRawTruncation(t *testing.T) {
	big := strings.Repeat("я", rawLimit) // 2 bytes per rune
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			enc := base64.RawURLEncoding.EncodeToString
			fmt.Fprint(w, enc([]byte(`{}`))+"."+enc(fmt.Appendf(nil, `{"exp":%d}`, time.Now().Add(time.Hour).Unix()))+".c2ln")
			return
		}
		fmt.Fprintf(w, `{"x":%q}`, big)
	}))
	defer g.Close()
	client, _ := garmin.New("k", garmin.WithBaseURL(g.URL))
	h := &handlers{svc: garmin.NewService(client, nil, time.UTC, nil)}
	res, _, err := h.apiGet(context.Background(), nil, RawInput{Endpoint: "profile_settings"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "truncated") || len(text) > rawLimit+200 {
		t.Fatalf("len = %d", len(text))
	}
	if !strings.HasPrefix(text, `{"x":"я`) {
		t.Fatal("bad prefix")
	}
}

func TestHeartRateBucketsOnDSTDay(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// 2026-03-29: clocks jump from 02:00 to 03:00 in Berlin.
	var vals []string
	for ts := time.Date(2026, 3, 29, 0, 0, 0, 0, berlin); ts.Before(time.Date(2026, 3, 30, 0, 0, 0, 0, berlin)); ts = ts.Add(30 * time.Minute) {
		vals = append(vals, fmt.Sprintf("[%d,60]", ts.UnixMilli()))
	}
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			enc := base64.RawURLEncoding.EncodeToString
			fmt.Fprint(w, enc([]byte(`{}`))+"."+enc(fmt.Appendf(nil, `{"exp":%d}`, time.Now().Add(time.Hour).Unix()))+".c2ln")
			return
		}
		fmt.Fprintf(w, `{"calendarDate":"2026-03-29","heartRateValues":[%s]}`, strings.Join(vals, ","))
	}))
	defer g.Close()
	client, _ := garmin.New("k", garmin.WithBaseURL(g.URL))
	h := &handlers{svc: garmin.NewService(client, nil, berlin, nil)}
	_, out, err := h.heartRate(context.Background(), nil, HeartRateInput{Date: "2026-03-29", IntervalMinutes: 240})
	if err != nil {
		t.Fatal(err)
	}
	var starts []string
	for _, b := range out.Series {
		starts = append(starts, b.Start)
	}
	if got := strings.Join(starts, ","); got != "00:00,04:00,08:00,12:00,16:00,20:00" {
		t.Fatalf("buckets = %s", got)
	}
}

func TestSensitiveEndpointsOptIn(t *testing.T) {
	if slices.Contains(RawEndpointNames(false), "menstrual_cycle_day") {
		t.Fatal("sensitive endpoint enabled by default")
	}
	if !slices.Contains(RawEndpointNames(true), "menstrual_cycle_day") {
		t.Fatal("sensitive endpoint missing when allowed")
	}
}

func TestSleepTimingAcrossMidnight(t *testing.T) {
	ms := func(h, m int) *int64 { v := time.Date(2026, 1, 1, h, m, 0, 0, time.UTC).UnixMilli(); return &v }
	next := func(h, m int) *int64 { v := time.Date(2026, 1, 2, h, m, 0, 0, time.UTC).UnixMilli(); return &v }
	cases := []struct {
		start, end *int64
		onset      string
		wake       string
	}{
		{ms(23, 30), next(7, 0), "23:30", "07:00"},
		{next(0, 45), next(8, 15), "00:45", "08:15"},
		{ms(20, 0), next(4, 0), "20:00", "04:00"},
	}
	for _, c := range cases {
		o, w, ok := sleepTiming(garmin.DailySleep{SleepStartTimestampLocal: c.start, SleepEndTimestampLocal: c.end})
		if !ok || clockFrom18(o) != c.onset || clockFrom18(w) != c.wake || w <= o {
			t.Errorf("got %s-%s (%v), want %s-%s", clockFrom18(o), clockFrom18(w), ok, c.onset, c.wake)
		}
	}
}
