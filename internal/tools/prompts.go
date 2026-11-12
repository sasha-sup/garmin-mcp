package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type promptDef struct {
	name, title, desc string
	defaultDays       int    // 0: the prompt takes no "days" argument
	body              string // %d = days when defaultDays > 0
}

var prompts = []promptDef{
	{
		name: "sleep_review", title: "Sleep quality review", defaultDays: 28,
		desc: "Analyse sleep quality over a period and give concrete, personal recommendations.",
		body: `Review my sleep over the last %d days using the Garmin tools.

1. Call garmin_sleep_analysis for the period. Call garmin_sleep for the worst nights if you need detail.
2. Assess: duration vs my sleep need, sleep debt, regularity (bedtime/wake spread, social jetlag), stage shares, awakenings, sleep heart rate, HRV and sleep stress, overnight SpO2 and breathing.
3. Use the "factors" section to find what in my day goes with worse sleep (stress, late or hard workouts, low activity, late bedtime). Say how strong the evidence is (number of nights).
4. Give at most 5 recommendations, ordered by expected impact. Each must cite my numbers and be specific (a time, a duration, a habit), e.g. "move bedtime from 00:40 to 23:30".
5. End with what to watch next week and which values, if any, deserve a doctor's opinion. This is not medical advice.`,
	},
	{
		name: "wellbeing_review", title: "Vital signs and wellbeing review", defaultDays: 28,
		desc: "Review resting heart rate, HRV, stress, Body Battery, activity and sleep together and suggest improvements.",
		body: `Review my vital signs and daily wellbeing over the last %d days using the Garmin tools.

1. Call garmin_trends (period week) for the context of the last 12 weeks, garmin_recovery_status for today, and garmin_daily_summary for the period.
2. Assess resting heart rate and HRV trends (direction and size of change), stress load and rest time, Body Battery recharge and drain, daily movement and intensity minutes (WHO target: 150 moderate-equivalent minutes per week), and sleep.
3. Point out patterns: days of the week, links between stress, sleep and next-day HRV/RHR.
4. Give at most 5 concrete recommendations ordered by expected impact, each tied to my numbers.
5. Flag anything that may warrant a doctor (e.g. persistently rising resting HR, very low SpO2). This is not medical advice.`,
	},
	{
		name: "readiness_check", title: "Readiness for today",
		desc: "Decide how hard to train or how much to push today based on recovery.",
		body: `Using garmin_recovery_status for today (and garmin_activities for the last 7 days if useful), tell me how recovered I am.
Answer in three parts: verdict (rest / easy / normal / hard is fine), the 2-3 metrics that drive the verdict with my numbers, and one practical suggestion for today and tonight's sleep.`,
	},
}

func registerPrompts(srv *mcp.Server) {
	for _, p := range prompts {
		prompt := &mcp.Prompt{Name: p.name, Title: p.title, Description: p.desc}
		if p.defaultDays > 0 {
			prompt.Arguments = []*mcp.PromptArgument{{
				Name:        "days",
				Description: fmt.Sprintf("Number of days to review (default %d).", p.defaultDays),
			}}
		}
		srv.AddPrompt(prompt, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			text, err := p.render(req.Params.Arguments["days"])
			if err != nil {
				return nil, err
			}
			return &mcp.GetPromptResult{
				Description: p.desc,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
			}, nil
		})
	}
}

func (p promptDef) render(daysArg string) (string, error) {
	if p.defaultDays == 0 {
		return p.body, nil
	}
	days := p.defaultDays
	if daysArg != "" {
		n, err := strconv.Atoi(daysArg)
		if err != nil || n < 1 || n > 366 {
			return "", errors.New("days must be a number between 1 and 366")
		}
		days = n
	}
	return fmt.Sprintf(p.body, days), nil
}
