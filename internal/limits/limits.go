// Package limits holds the sending policy of a session: pacing between messages,
// daily caps and the warm-up of new numbers. It is pure logic with no I/O.
//
// The default values are starting points to tune per deployment, not official
// WhatsApp figures.
package limits

import (
	"errors"
	"math"
	"time"
)

// Priority is the kind of traffic a message belongs to.
type Priority string

// Priorities.
const (
	Transactional Priority = "transactional"
	Promotional   Priority = "promotional"
)

// Policy is the sending policy applied to every session.
type Policy struct {
	// Transactional traffic (invoices, receipts): high volume, short pacing.
	TransactionalInterval time.Duration
	TransactionalDailyCap int

	// Promotional traffic: random delay between messages and a low daily cap.
	PromotionalMinDelay time.Duration
	PromotionalMaxDelay time.Duration
	PromotionalDailyCap int

	// WarmupDays is how many days a new number takes to reach the full
	// promotional cap, growing linearly from 1/WarmupDays of it.
	WarmupDays int

	// Location defines when a "day" starts for the daily caps.
	Location *time.Location
}

// Default returns the starting-point policy.
func Default() Policy {
	return Policy{
		TransactionalInterval: time.Second,
		TransactionalDailyCap: 2000,
		PromotionalMinDelay:   8 * time.Second,
		PromotionalMaxDelay:   20 * time.Second,
		PromotionalDailyCap:   200,
		WarmupDays:            7,
		Location:              time.UTC,
	}
}

// Validate reports an inconsistent policy.
func (p Policy) Validate() error {
	switch {
	case p.TransactionalInterval < 0 || p.PromotionalMinDelay < 0:
		return errors.New("delays must not be negative")
	case p.PromotionalMaxDelay < p.PromotionalMinDelay:
		return errors.New("promotional max delay must be at least the min delay")
	case p.TransactionalDailyCap <= 0 || p.PromotionalDailyCap <= 0:
		return errors.New("daily caps must be positive")
	case p.WarmupDays < 1:
		return errors.New("warmup days must be at least 1")
	case p.Location == nil:
		return errors.New("location is required")
	}
	return nil
}

// Gap is the time that must pass after the previous send before a message of
// the given priority may go out. random is a value in [0,1) that picks the
// promotional delay; it is injected so the policy stays deterministic in tests.
func (p Policy) Gap(priority Priority, random float64) time.Duration {
	if priority == Promotional {
		spread := p.PromotionalMaxDelay - p.PromotionalMinDelay
		return p.PromotionalMinDelay + time.Duration(random*float64(spread))
	}
	return p.TransactionalInterval
}

// DailyCap is the number of messages of a priority a session may send today.
// Only promotional traffic goes through warm-up, counted from the first time the
// number connected (a zero time means it has just connected).
func (p Policy) DailyCap(priority Priority, firstConnected, now time.Time) int {
	if priority != Promotional {
		return p.TransactionalDailyCap
	}
	day := 0
	if !firstConnected.IsZero() && now.After(firstConnected) {
		day = int(now.Sub(firstConnected) / (24 * time.Hour))
	}
	if day >= p.WarmupDays {
		return p.PromotionalDailyCap
	}
	return int(math.Ceil(float64(p.PromotionalDailyCap) * float64(day+1) / float64(p.WarmupDays)))
}

// DayStart is the beginning of the current day in the policy's location.
func (p Policy) DayStart(now time.Time) time.Time {
	local := now.In(p.Location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, p.Location)
}

// NextDayStart is the beginning of the next day in the policy's location.
func (p Policy) NextDayStart(now time.Time) time.Time {
	return p.DayStart(now).AddDate(0, 0, 1)
}
