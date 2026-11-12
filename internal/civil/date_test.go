package civil

import (
	"testing"
	"time"
)

func TestParseAndString(t *testing.T) {
	d, err := Parse("2026-02-28")
	if err != nil || d.String() != "2026-02-28" {
		t.Fatalf("%v %v", d, err)
	}
	for _, bad := range []string{"", "2026-2-28", "2026-02-30", "28.02.2026", "2026-02-28T00:00:00Z"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

func TestArithmetic(t *testing.T) {
	d, _ := Parse("2024-02-28")
	if got := d.AddDays(1).String(); got != "2024-02-29" {
		t.Errorf("leap day: %s", got)
	}
	e, _ := Parse("2024-03-31")
	if n := d.DaysUntil(e); n != 32 {
		t.Errorf("DaysUntil = %d", n)
	}
	if n := e.DaysUntil(d); n != -32 {
		t.Errorf("DaysUntil reverse = %d", n)
	}
	if r := Range(d, e); len(r) != 33 || r[0].String() != "2024-02-28" || r[32].String() != "2024-03-31" {
		t.Errorf("Range = %d items", len(r))
	}
	if Range(e, d) != nil {
		t.Error("reversed Range not nil")
	}
	if got := e.Month().String(); got != "2024-03-01" {
		t.Errorf("Month = %s", got)
	}
}

func TestEndAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	d, _ := Parse("2026-03-29") // 23-hour day
	want := time.Date(2026, 3, 30, 0, 0, 0, 0, berlin)
	if got := d.End(berlin); !got.Equal(want) {
		t.Errorf("End = %v, want %v", got, want)
	}
}

func TestTodayUsesLocation(t *testing.T) {
	now := time.Date(2026, 1, 1, 22, 30, 0, 0, time.UTC)
	tbilisi := time.FixedZone("+04", 4*3600)
	if got := Today(now, tbilisi).String(); got != "2026-01-02" {
		t.Errorf("Today = %s", got)
	}
}
