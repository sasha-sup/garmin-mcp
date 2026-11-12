package garmin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sasha-sup/garmin-mcp/internal/cache"
	"github.com/sasha-sup/garmin-mcp/internal/civil"
)

func mustDate(t *testing.T, s string) civil.Date {
	t.Helper()
	d, err := civil.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sleepFake answers the sleep range endpoint with one item per requested day
// except days listed in empty, and records each requested range.
func sleepFake(t *testing.T, empty map[string]bool) (*fake, *[][2]string) {
	var mu sync.Mutex
	var ranges [][2]string
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sleep-service/sleep/dailySleepSummariesByDate" {
			http.NotFound(w, r)
			return
		}
		from, _ := civil.Parse(r.URL.Query().Get("startDate"))
		to, _ := civil.Parse(r.URL.Query().Get("endDate"))
		if from.DaysUntil(to) >= 31 {
			w.WriteHeader(http.StatusBadRequest) // mirrors the real limit
			return
		}
		mu.Lock()
		ranges = append(ranges, [2]string{from.String(), to.String()})
		mu.Unlock()
		var items []string
		for _, d := range civil.Range(from, to) {
			if !empty[d.String()] {
				items = append(items, fmt.Sprintf(`{"calendarDate":%q,"sleepTimeSeconds":28800}`, d))
			}
		}
		fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	})
	return f, &ranges
}

// newTestService pins "today" to 2020-03-01 so test dates are settled.
func newTestService(t *testing.T, f *fake, withCache bool) *Service {
	t.Helper()
	var ch *cache.Cache
	if withCache {
		var err error
		if ch, err = cache.New(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(newTestClient(t, f), ch, time.UTC, nil)
	now, _ := time.Parse(time.DateOnly, "2020-03-01")
	s.now = func() time.Time { return now.Add(12 * time.Hour) }
	return s
}

func TestDailyChunksRespectSpanLimit(t *testing.T) {
	f, ranges := sleepFake(t, nil)
	s := newTestService(t, f, false)
	m, err := s.Sleep(context.Background(), mustDate(t, "2020-01-01"), mustDate(t, "2020-02-29"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 60 {
		t.Fatalf("got %d days, want 60", len(m))
	}
	if len(*ranges) != 2 {
		t.Fatalf("requests = %v, want 2 chunks", *ranges)
	}
}

func TestDailyCacheServesSettledDays(t *testing.T) {
	f, ranges := sleepFake(t, map[string]bool{"2020-01-03": true})
	s := newTestService(t, f, true)
	ctx := context.Background()
	from, to := mustDate(t, "2020-01-01"), mustDate(t, "2020-01-05")
	first, err := s.Sleep(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Sleep(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(*ranges) != 1 {
		t.Fatalf("requests = %v, want 1 (second call from cache)", *ranges)
	}
	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("days = %d/%d, want 4 (one day has no data)", len(first), len(second))
	}
	// A wider range fetches only the new days.
	if _, err := s.Sleep(ctx, from, mustDate(t, "2020-01-07")); err != nil {
		t.Fatal(err)
	}
	if got := (*ranges)[1]; got != [2]string{"2020-01-06", "2020-01-07"} {
		t.Fatalf("second request = %v", got)
	}
}

func TestDailyRecentDaysExpire(t *testing.T) {
	f, ranges := sleepFake(t, nil)
	s := newTestService(t, f, true)
	ctx := context.Background()
	// Pretend "now" is the real clock so cache mtimes are recent relative to it.
	s.now = time.Now
	today := s.Today()
	if _, err := s.Sleep(ctx, today, today); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sleep(ctx, today, today); err != nil {
		t.Fatal(err)
	}
	if len(*ranges) != 1 {
		t.Fatalf("within freshTTL the cache must be used, requests = %v", *ranges)
	}
	s.now = func() time.Time { return time.Now().Add(freshTTL + time.Minute) }
	if _, err := s.Sleep(ctx, today, today); err != nil {
		t.Fatal(err)
	}
	if len(*ranges) != 2 {
		t.Fatalf("after freshTTL today must be refetched, requests = %v", *ranges)
	}
}

func TestDailyClampsFuture(t *testing.T) {
	f, ranges := sleepFake(t, nil)
	s := newTestService(t, f, false)
	if _, err := s.Sleep(context.Background(), mustDate(t, "2020-02-28"), mustDate(t, "2020-03-10")); err != nil {
		t.Fatal(err)
	}
	if got := (*ranges)[0]; got != [2]string{"2020-02-28", "2020-03-01"} {
		t.Fatalf("request = %v, want clamped to today", got)
	}
}

func TestChunk(t *testing.T) {
	d := mustDate(t, "2020-01-01")
	days := []civil.Date{d, d.AddDays(1), d.AddDays(2), d.AddDays(5), d.AddDays(6)}
	got := chunk(days, 2)
	want := [][]string{{"2020-01-01", "2020-01-02"}, {"2020-01-03"}, {"2020-01-06", "2020-01-07"}}
	if len(got) != len(want) {
		t.Fatalf("chunks = %v", got)
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j].String() != want[i][j] {
				t.Fatalf("chunks = %v, want %v", got, want)
			}
		}
	}
}

func TestActivitiesPaging(t *testing.T) {
	total := 250
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var items []string
		for i := start; i < min(start+limit, total); i++ {
			items = append(items, fmt.Sprintf(`{"activityId":%d,"startTimeLocal":"2020-01-01 10:00:00"}`, i+1))
		}
		fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	})
	s := newTestService(t, f, false)
	for _, tc := range []struct{ max, want int }{{20, 20}, {150, 150}, {1000, 250}} {
		got, err := s.Activities(context.Background(), mustDate(t, "2020-01-01"), mustDate(t, "2020-01-31"), "", "", tc.max)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("max=%d: got %d, want %d", tc.max, len(got), tc.want)
		}
	}
}

func TestFreshRule(t *testing.T) {
	s := NewService(nil, nil, time.UTC, nil)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	day := civil.Of(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) // ended 2026-06-02 00:00
	end := day.End(time.UTC)
	cases := []struct {
		name    string
		written time.Time
		empty   bool
		want    bool
	}{
		{"settled data", end.Add(settleAfter + time.Hour), false, true},
		{"unsettled, recent", now.Add(-freshTTL / 2), false, true},
		{"unsettled, stale", end.Add(settleAfter - time.Hour), false, false},
		{"empty, fetched late but not settled", end.Add(5 * 24 * time.Hour), true, false},
		{"empty, recent", now.Add(-emptyTTL / 2), true, true},
		{"empty, settled", end.Add(emptySettleAfter + time.Hour), true, true},
	}
	for _, c := range cases {
		if got := s.fresh(day, c.written, c.empty); got != c.want {
			t.Errorf("%s: fresh = %v, want %v", c.name, got, c.want)
		}
	}
}

// A day fetched before the watch synced must not stay empty forever.
func TestEmptyDayIsRefetched(t *testing.T) {
	var synced atomic.Bool
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if !synced.Load() {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"calendarDate":%q,"sleepTimeSeconds":28800}]`, r.URL.Query().Get("startDate"))
	})
	ch, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(newTestClient(t, f), ch, time.UTC, nil)
	day := s.Today().AddDays(-5) // ended long enough ago to count as settled if it had data
	ctx := context.Background()

	if m, err := s.Sleep(ctx, day, day); err != nil || len(m) != 0 {
		t.Fatalf("first fetch: %v, %v", m, err)
	}
	synced.Store(true)
	// Age the "no data" entry past emptyTTL.
	p := filepath.Join(ch.Dir(), "sleep", day.String()+".json")
	old := time.Now().Add(-emptyTTL - time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	m, err := s.Sleep(ctx, day, day)
	if err != nil || len(m) != 1 {
		t.Fatalf("after sync: %v, %v (empty day was served from cache)", m, err)
	}
	// Now it has data and was fetched > settleAfter after the day: cached for good.
	before := f.dataCalls.Load()
	if _, err := s.Sleep(ctx, day, day); err != nil {
		t.Fatal(err)
	}
	if f.dataCalls.Load() != before {
		t.Fatal("settled day was refetched")
	}
}
