//go:build live

// Live smoke test against the real Garmin API. It never runs by default.
//
//	GARMIN_ENV_FILE=.env go test -tags live -run TestLive -v ./internal/tools/
//
// It prints only sizes and error states, never the health data itself.
package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/config"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
)

func TestLive(t *testing.T) {
	if f := os.Getenv("GARMIN_ENV_FILE"); f != "" {
		if _, err := config.LoadEnvFile(f); err != nil {
			t.Fatal(err)
		}
	}
	key := os.Getenv(config.EnvAPIKey)
	if key == "" {
		key = os.Getenv(config.EnvAPIKeyAlias)
	}
	if key == "" {
		t.Skip("GARMIN_API_KEY not set (or pass GARMIN_ENV_FILE=.env)")
	}
	client, err := garmin.New(key)
	if err != nil {
		t.Fatal(err)
	}
	loc := time.Local
	if tz := os.Getenv("GARMIN_TZ"); tz != "" {
		if loc, err = time.LoadLocation(tz); err != nil {
			t.Fatal(err)
		}
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "garmin-mcp", Version: "live"}, nil)
	Register(srv, garmin.NewService(client, nil, loc, nil), Options{})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "live", Version: "live"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"garmin_daily_summary", map[string]any{"days": 30}},
		{"garmin_sleep", map[string]any{"days": 40}},
		{"garmin_sleep_analysis", map[string]any{"days": 60}},
		{"garmin_hrv", map[string]any{"days": 30}},
		{"garmin_body_battery", map[string]any{"days": 3, "include_timeline": true}},
		{"garmin_heart_rate", nil},
		{"garmin_activities", map[string]any{"days": 90, "limit": 50}},
		{"garmin_recovery_status", nil},
		{"garmin_trends", map[string]any{"days": 180}},
		{"garmin_trends", map[string]any{"days": 365, "period": "month"}},
		{"garmin_profile", nil},
		{"garmin_weight", map[string]any{"days": 365}},
		{"garmin_api_get", map[string]any{"endpoint": "stress_daily", "start_date": time.Now().AddDate(0, 0, -6).Format(time.DateOnly), "end_date": time.Now().Format(time.DateOnly)}},
	}
	var firstActivity int64
	for _, c := range cases {
		start := time.Now()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Errorf("%s: protocol error: %v", c.name, err)
			continue
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			t.Errorf("%s: tool error: %s", c.name, text)
			continue
		}
		t.Logf("%-24s ok  %6d bytes  %v", c.name, len(text), time.Since(start).Round(time.Millisecond))
		if c.name == "garmin_activities" {
			var out ActivitiesOut
			if json.Unmarshal([]byte(text), &out) == nil && len(out.Activities) > 0 {
				firstActivity = out.Activities[0].ID
			}
		}
	}
	if firstActivity != 0 {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "garmin_activity", Arguments: map[string]any{"activity_id": firstActivity}})
		if err != nil || res.IsError {
			t.Errorf("garmin_activity failed: %v", err)
		} else {
			t.Logf("%-24s ok", "garmin_activity")
		}
	}
}
