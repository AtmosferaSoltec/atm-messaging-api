package limits

import (
	"testing"
	"time"
)

func TestGap(t *testing.T) {
	p := Default()

	if got := p.Gap(Transactional, 0.9); got != p.TransactionalInterval {
		t.Errorf("transactional gap is fixed, got %v", got)
	}
	if got := p.Gap(Promotional, 0); got != 8*time.Second {
		t.Errorf("lowest promotional gap should be the min delay, got %v", got)
	}
	if got := p.Gap(Promotional, 0.5); got != 14*time.Second {
		t.Errorf("middle promotional gap should be 14s, got %v", got)
	}
	if got := p.Gap(Promotional, 0.999999); got < 19*time.Second || got >= 20*time.Second {
		t.Errorf("promotional gap must stay below the max delay, got %v", got)
	}
}

func TestDailyCapWarmup(t *testing.T) {
	p := Default() // promotional cap 200 over 7 days
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"first moments", start, 29},
		{"same day", start.Add(23 * time.Hour), 29},
		{"second day", start.Add(25 * time.Hour), 58},
		{"day four", start.Add(3*24*time.Hour + time.Hour), 115},
		{"last warm-up day", start.Add(6*24*time.Hour + time.Hour), 200},
		{"after warm-up", start.Add(30 * 24 * time.Hour), 200},
	}
	for _, tc := range cases {
		if got := p.DailyCap(Promotional, start, tc.now); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}

	if got := p.DailyCap(Promotional, time.Time{}, start); got != 29 {
		t.Errorf("an unknown start counts as day one, got %d", got)
	}
	if got := p.DailyCap(Transactional, start, start); got != p.TransactionalDailyCap {
		t.Errorf("transactional traffic skips warm-up, got %d", got)
	}
}

func TestDailyCapNeverExceedsConfiguredCap(t *testing.T) {
	p := Default()
	p.PromotionalDailyCap = 5
	p.WarmupDays = 10
	start := time.Now()
	for day := 0; day < 15; day++ {
		got := p.DailyCap(Promotional, start, start.Add(time.Duration(day)*24*time.Hour+time.Minute))
		if got < 1 || got > 5 {
			t.Fatalf("day %d: cap %d out of range", day, got)
		}
	}
}

func TestDayBoundariesUseTheConfiguredLocation(t *testing.T) {
	lima, err := time.LoadLocation("America/Lima")
	if err != nil {
		t.Skip("tzdata not available")
	}
	p := Default()
	p.Location = lima

	// 03:00 UTC is 22:00 of the previous day in Lima (UTC-5).
	now := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 3, 9, 0, 0, 0, 0, lima)
	if got := p.DayStart(now); !got.Equal(wantStart) {
		t.Errorf("day start: got %v, want %v", got, wantStart)
	}
	if got := p.NextDayStart(now); !got.Equal(wantStart.AddDate(0, 0, 1)) {
		t.Errorf("next day start: got %v", got)
	}
}

func TestValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default policy must be valid: %v", err)
	}
	breakers := map[string]func(*Policy){
		"max below min":   func(p *Policy) { p.PromotionalMaxDelay = time.Second },
		"zero cap":        func(p *Policy) { p.PromotionalDailyCap = 0 },
		"no warm-up days": func(p *Policy) { p.WarmupDays = 0 },
		"negative delay":  func(p *Policy) { p.TransactionalInterval = -time.Second },
		"no location":     func(p *Policy) { p.Location = nil },
	}
	for name, mutate := range breakers {
		p := Default()
		mutate(&p)
		if p.Validate() == nil {
			t.Errorf("%s should be invalid", name)
		}
	}
}
