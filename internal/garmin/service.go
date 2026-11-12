package garmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/sasha-sup/garmin-mcp/internal/cache"
	"github.com/sasha-sup/garmin-mcp/internal/civil"
)

// Cache policy.
//
// Garmin keeps updating a day while the watch syncs, and the sleep of night N
// is attached to day N+1. A day counts as settled once it was fetched more
// than settleAfter after the day ended; settled days are served from cache
// forever, the rest for at most freshTTL.
const (
	settleAfter = 48 * time.Hour
	freshTTL    = 10 * time.Minute
	metaTTL     = 6 * time.Hour  // profile, zones, fitness age
	activityTTL = 24 * time.Hour // single activity details
)

// nullJSON marks "Garmin returned nothing for this day" in the cache.
var nullJSON = []byte("null")

// dailyKind describes a per-day resource and how to fetch a range of it.
type dailyKind struct {
	name    string
	maxSpan int // days per request; Garmin answers 400 above the limit
	fetch   func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error)
}

// Service is the cached, range-aware data layer over Client.
type Service struct {
	c     *Client
	cache *cache.Cache
	loc   *time.Location
	now   func() time.Time
	log   *slog.Logger
}

// NewService builds a Service. cache may be nil to disable disk caching.
func NewService(c *Client, ch *cache.Cache, loc *time.Location, log *slog.Logger) *Service {
	if loc == nil {
		loc = time.Local
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{c: c, cache: ch, loc: loc, now: time.Now, log: log}
}

// Today returns the current date in the user's time zone.
func (s *Service) Today() civil.Date { return civil.Today(s.now(), s.loc) }

// Location returns the user's time zone.
func (s *Service) Location() *time.Location { return s.loc }

// Client exposes the underlying API client.
func (s *Service) Client() *Client { return s.c }

var (
	kindSummary = dailyKind{name: "summary", maxSpan: 365, fetch: func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error) {
		var resp struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := getJSON(ctx, c, "/usersummary-service/v2/dailysummaries", rangeQuery(from, to), &resp); err != nil {
			return nil, err
		}
		return splitByDate(resp.Data, "calendarDate")
	}}
	kindSleep = dailyKind{name: "sleep", maxSpan: 31, fetch: func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error) {
		var resp []json.RawMessage
		if err := getJSON(ctx, c, "/sleep-service/sleep/dailySleepSummariesByDate", rangeQuery(from, to), &resp); err != nil {
			return nil, err
		}
		return splitByDate(resp, "calendarDate")
	}}
	// The HRV range limit is undocumented; 365 days worked in testing, 90 is
	// a safe margin.
	kindHRV = dailyKind{name: "hrv", maxSpan: 90, fetch: func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error) {
		var resp struct {
			Summaries []json.RawMessage `json:"hrvSummaries"`
		}
		if err := getJSON(ctx, c, "/hrv-service/hrv/daily/"+from.String()+"/"+to.String(), nil, &resp); err != nil {
			return nil, err
		}
		return splitByDate(resp.Summaries, "calendarDate")
	}}
	kindBodyBattery = dailyKind{name: "bodybattery", maxSpan: 31, fetch: func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error) {
		var resp []json.RawMessage
		if err := getJSON(ctx, c, "/wellness-service/wellness/bodyBattery/reports/daily", rangeQuery(from, to), &resp); err != nil {
			return nil, err
		}
		return splitByDate(resp, "date")
	}}
	kindHeartRate = dailyKind{name: "heartrate", maxSpan: 1, fetch: func(ctx context.Context, c *Client, from, _ civil.Date) (map[string]json.RawMessage, error) {
		body, err := c.Get(ctx, "/wellness-service/wellness/dailyHeartRate", url.Values{"date": {from.String()}})
		if err != nil {
			return nil, err
		}
		if !json.Valid(body) {
			return nil, fmt.Errorf("garmin: invalid JSON from dailyHeartRate")
		}
		return map[string]json.RawMessage{from.String(): body}, nil
	}}
	kindWeight = dailyKind{name: "weight", maxSpan: 90, fetch: func(ctx context.Context, c *Client, from, to civil.Date) (map[string]json.RawMessage, error) {
		var resp struct {
			Daily []json.RawMessage `json:"dailyWeightSummaries"`
		}
		if err := getJSON(ctx, c, "/weight-service/weight/range/"+from.String()+"/"+to.String(), nil, &resp); err != nil {
			return nil, err
		}
		return splitByDate(resp.Daily, "summaryDate")
	}}
)

// DailySummaries returns one summary per day that has data.
func (s *Service) DailySummaries(ctx context.Context, from, to civil.Date) (map[string]DailySummary, error) {
	return daily[DailySummary](ctx, s, kindSummary, from, to)
}

// Sleep returns sleep data keyed by the calendar date of waking up.
func (s *Service) Sleep(ctx context.Context, from, to civil.Date) (map[string]DailySleep, error) {
	return daily[DailySleep](ctx, s, kindSleep, from, to)
}

// HRV returns overnight HRV summaries.
func (s *Service) HRV(ctx context.Context, from, to civil.Date) (map[string]HRVSummary, error) {
	return daily[HRVSummary](ctx, s, kindHRV, from, to)
}

// BodyBattery returns Body Battery reports.
func (s *Service) BodyBattery(ctx context.Context, from, to civil.Date) (map[string]BodyBatteryReport, error) {
	return daily[BodyBatteryReport](ctx, s, kindBodyBattery, from, to)
}

// HeartRate returns the intraday heart rate of one day.
func (s *Service) HeartRate(ctx context.Context, day civil.Date) (*DailyHeartRate, error) {
	m, err := daily[DailyHeartRate](ctx, s, kindHeartRate, day, day)
	if err != nil {
		return nil, err
	}
	if v, ok := m[day.String()]; ok {
		return &v, nil
	}
	return nil, nil
}

// Weight returns the daily weight summaries.
func (s *Service) Weight(ctx context.Context, from, to civil.Date) (map[string]WeightSample, error) {
	type day struct {
		Latest *WeightSample `json:"latestWeight"`
	}
	m, err := daily[day](ctx, s, kindWeight, from, to)
	if err != nil {
		return nil, err
	}
	out := make(map[string]WeightSample, len(m))
	for k, v := range m {
		if v.Latest != nil {
			out[k] = *v.Latest
		}
	}
	return out, nil
}

// Activities lists activities that started between from and to (inclusive,
// local dates), newest first. It pages through the results up to limit items.
func (s *Service) Activities(ctx context.Context, from, to civil.Date, activityType, search string, limit int) ([]Activity, error) {
	const page = 100
	var out []Activity
	for start := 0; len(out) < limit; start += page {
		want := min(page, limit-len(out))
		q := url.Values{
			"startDate": {from.String()},
			"endDate":   {to.String()},
			"start":     {strconv.Itoa(start)},
			"limit":     {strconv.Itoa(want)},
		}
		if activityType != "" {
			q.Set("activityType", activityType)
		}
		if search != "" {
			q.Set("search", search)
		}
		var batch []Activity
		err := getJSON(ctx, s.c, "/activitylist-service/activities/search/activities", q, &batch)
		if errors.Is(err, ErrNoData) {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < want {
			break
		}
	}
	return out, nil
}

// Activity returns the summary of one activity.
func (s *Service) Activity(ctx context.Context, id int64) (*ActivityDetail, error) {
	var out ActivityDetail
	path := "/activity-service/activity/" + strconv.FormatInt(id, 10)
	if err := s.cachedGet(ctx, "activity/"+strconv.FormatInt(id, 10), activityTTL, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Profile returns the user's profile settings.
func (s *Service) Profile(ctx context.Context) (*ProfileSettings, error) {
	var out ProfileSettings
	if err := s.cachedGet(ctx, "meta/profile", metaTTL, "/userprofile-service/userprofile/settings", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HeartRateZones returns configured heart rate zones per sport.
func (s *Service) HeartRateZones(ctx context.Context) ([]HeartRateZones, error) {
	var out []HeartRateZones
	if err := s.cachedGet(ctx, "meta/hrzones", metaTTL, "/biometric-service/heartRateZones/v2", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FitnessAge returns the latest Fitness Age as of day.
func (s *Service) FitnessAge(ctx context.Context, day civil.Date) (*FitnessAge, error) {
	var out FitnessAge
	err := s.cachedGet(ctx, "meta/fitnessage-"+day.String(), metaTTL, "/fitnessage-service/fitnessage/v2/"+day.String(), nil, &out)
	if errors.Is(err, ErrNoData) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// cachedGet fetches path, caching the raw body under key for ttl.
func (s *Service) cachedGet(ctx context.Context, key string, ttl time.Duration, path string, q url.Values, v any) error {
	if data, written, ok := s.cache.Get(key); ok && s.now().Sub(written) < ttl {
		if err := json.Unmarshal(data, v); err == nil {
			return nil
		}
	}
	body, err := s.c.Get(ctx, path, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("garmin: decode %s: %w", path, err)
	}
	s.putCache(key, body)
	return nil
}

func (s *Service) putCache(key string, data []byte) {
	if err := s.cache.Put(key, data); err != nil {
		s.log.Warn("cache write failed", "key", key, "err", err)
	}
}

// fresh reports whether a cached entry for day, written at written, can be
// served without asking Garmin again.
func (s *Service) fresh(day civil.Date, written time.Time) bool {
	if written.Sub(day.End(s.loc)) >= settleAfter {
		return true
	}
	return s.now().Sub(written) < freshTTL
}

// daily returns decoded per-day items for [from, to], combining cache hits
// with chunked, parallel API requests for the missing days.
func daily[T any](ctx context.Context, s *Service, k dailyKind, from, to civil.Date) (map[string]T, error) {
	if today := s.Today(); to.After(today) {
		to = today // the future has no data; don't cache "nothing" for it
	}
	days := civil.Range(from, to)
	raw := make(map[string]json.RawMessage, len(days))
	var missing []civil.Date
	for _, d := range days {
		if data, written, ok := s.cache.Get(k.name + "/" + d.String()); ok && s.fresh(d, written) {
			raw[d.String()] = data
			continue
		}
		missing = append(missing, d)
	}

	if len(missing) > 0 {
		chunks := chunk(missing, k.maxSpan)
		var (
			mu   sync.Mutex
			wg   sync.WaitGroup
			errs []error
		)
		for _, ch := range chunks {
			wg.Go(func() {
				got, err := k.fetch(ctx, s.c, ch[0], ch[len(ch)-1])
				if errors.Is(err, ErrNoData) {
					got, err = map[string]json.RawMessage{}, nil
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, d := range ch {
					data, ok := got[d.String()]
					if !ok {
						data = nullJSON
					}
					raw[d.String()] = data
					s.putCache(k.name+"/"+d.String(), data)
				}
			})
		}
		wg.Wait()
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
	}

	out := make(map[string]T, len(raw))
	for day, data := range raw {
		if bytes.Equal(bytes.TrimSpace(data), nullJSON) {
			continue
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("garmin: decode %s for %s: %w", k.name, day, err)
		}
		out[day] = v
	}
	return out, nil
}

// chunk groups sorted dates into runs of consecutive days no longer than span.
func chunk(days []civil.Date, span int) [][]civil.Date {
	var out [][]civil.Date
	var cur []civil.Date
	for _, d := range days {
		if len(cur) > 0 && (len(cur) >= span || !cur[len(cur)-1].AddDays(1).Equal(d)) {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, d)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func rangeQuery(from, to civil.Date) url.Values {
	return url.Values{"startDate": {from.String()}, "endDate": {to.String()}}
}

func getJSON(ctx context.Context, c *Client, path string, q url.Values, v any) error {
	body, err := c.Get(ctx, path, q)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return ErrNoData
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("garmin: decode %s: %w", path, err)
	}
	return nil
}

// splitByDate maps each item to the value of its date field.
func splitByDate(items []json.RawMessage, field string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(items))
	for _, it := range items {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(it, &probe); err != nil {
			return nil, fmt.Errorf("garmin: decode item: %w", err)
		}
		var date string
		if err := json.Unmarshal(probe[field], &date); err != nil || date == "" {
			continue // items without a date cannot be placed
		}
		out[date] = it
	}
	return out, nil
}
