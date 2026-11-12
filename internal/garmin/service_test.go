package garmin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
func sleepFake(t *testing.T, empty map[string]bool) (*fake, *[][2]string, *sync.Mutex) {
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
	return f, &ranges, &mu
}

func newTestService(t *testing.T, f *fake, today string, withCache bool) *Service {
	t.Helper()
	var ch *cache.Cache
	if withCache {
		var err error
		if ch, err = cache.New(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(newTestClient(t, f), ch, time.UTC, nil)
	now, _ := time.Parse(time.DateOnly, today)
	s.now = func() time.Time { return now.Add(12 * time.Hour) }
	return s
}

func TestDailyChunksRespectSpanLimit(t *testing.T) {
	f, ranges, _ := sleepFake(t, nil)
	s := newTestService(t, f, "2020-03-01", false)
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
	f, ranges, _ := sleepFake(t, map[string]bool{"2020-01-03": true})
	s := newTestService(t, f, "2020-03-01", true)
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
	f, ranges, _ := sleepFake(t, nil)
	s := newTestService(t, f, "2020-03-01", true)
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
	f, ranges, _ := sleepFake(t, nil)
	s := newTestService(t, f, "2020-03-01", false)
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
	s := newTestService(t, f, "2020-03-01", false)
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
