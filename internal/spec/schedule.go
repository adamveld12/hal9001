package spec

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Schedule is when a sensor polls: a fixed interval or a cron expression. The
// zero Schedule (neither) is manual: the scheduler never polls the sensor and
// only an explicit action ("Run now", `sensors poll`) does.
type Schedule struct {
	Interval time.Duration
	Cron     *Cron
}

// Manual reports whether the schedule never fires on its own.
func (s Schedule) Manual() bool { return s.Interval == 0 && s.Cron == nil }

// Schedule returns the sensor's parsed schedule. At most one of interval and
// cron may be set; with neither, the sensor is manual-only.
func (s *Sensor) Schedule() (Schedule, error) {
	switch {
	case s.Interval != 0 && s.Cron != "":
		return Schedule{}, errors.New("set either interval or cron, not both")
	case s.Interval != 0:
		if s.Interval < Duration(time.Second) {
			return Schedule{}, fmt.Errorf("interval %s is below the 1s minimum", s.Interval)
		}
		return Schedule{Interval: s.Interval.Std()}, nil
	case s.Cron != "":
		c, err := ParseCron(s.Cron)
		if err != nil {
			return Schedule{}, err
		}
		return Schedule{Cron: c}, nil
	default:
		return Schedule{}, nil
	}
}

// Next returns the next poll time strictly after after. For an interval
// schedule that is after+Interval (the scheduler decides what "after" is).
// The zero time means the schedule never fires again.
func (s Schedule) Next(after time.Time) time.Time {
	if s.Cron != nil {
		return s.Cron.Next(after)
	}
	return after.Add(s.Interval)
}

// Cron is a parsed 5-field cron expression: minute hour day-of-month month
// day-of-week, with the usual Vixie semantics: lists (1,5), ranges (1-5),
// steps (*/15, 10-30/5), month and weekday names (jan, mon), weekday 0 or 7 as
// Sunday, and OR-matching of day-of-month and day-of-week when both are
// restricted. Times are evaluated in the location of the time passed to Next.
type Cron struct {
	expr                     string
	minute, hour, dom, month uint64
	dow                      uint64
	domStar, dowStar         bool
}

type cronField struct {
	name     string
	min, max int
	names    []string // names[i] is the name of value min+i
}

var (
	cronMinute = cronField{name: "minute", min: 0, max: 59}
	cronHour   = cronField{name: "hour", min: 0, max: 23}
	cronDOM    = cronField{name: "day-of-month", min: 1, max: 31}
	cronMonth  = cronField{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}}
	cronDOW    = cronField{name: "day-of-week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}}
)

// ParseCron parses a 5-field cron expression.
func ParseCron(expr string) (*Cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields (minute hour day-of-month month day-of-week), got %d", expr, len(f))
	}
	c := &Cron{expr: strings.Join(f, " ")}
	var err error
	if c.minute, _, err = parseCronField(f[0], cronMinute); err != nil {
		return nil, err
	}
	if c.hour, _, err = parseCronField(f[1], cronHour); err != nil {
		return nil, err
	}
	if c.dom, c.domStar, err = parseCronField(f[2], cronDOM); err != nil {
		return nil, err
	}
	if c.month, _, err = parseCronField(f[3], cronMonth); err != nil {
		return nil, err
	}
	if c.dow, c.dowStar, err = parseCronField(f[4], cronDOW); err != nil {
		return nil, err
	}
	// 7 is an alias for Sunday.
	if c.dow&(1<<7) != 0 {
		c.dow = c.dow&^(1<<7) | 1
	}
	if c.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).IsZero() {
		return nil, fmt.Errorf("cron %q never matches a real date", expr)
	}
	return c, nil
}

func (c *Cron) String() string { return c.expr }

func parseCronField(s string, f cronField) (set uint64, star bool, err error) {
	fail := func(format string, args ...any) (uint64, bool, error) {
		return 0, false, fmt.Errorf("cron %s field %q: %s", f.name, s, fmt.Sprintf(format, args...))
	}
	if s == "" {
		return fail("empty")
	}
	star = strings.HasPrefix(s, "*")
	for part := range strings.SplitSeq(s, ",") {
		if part == "" {
			return fail("empty list element")
		}
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, perr := strconv.Atoi(stepStr)
			if perr != nil || n <= 0 {
				return fail("invalid step %q", stepStr)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var e1, e2 error
			lo, e1 = f.value(a)
			hi, e2 = f.value(b)
			if e := errors.Join(e1, e2); e != nil {
				return fail("%v", e)
			}
			if lo > hi {
				return fail("range %d-%d is backwards", lo, hi)
			}
		default:
			v, verr := f.value(rng)
			if verr != nil {
				return fail("%v", verr)
			}
			lo = v
			if hasStep {
				hi = f.max // N/step means N through the maximum
			} else {
				hi = v
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	if bits.OnesCount64(set) == 0 {
		return fail("matches nothing")
	}
	return set, star, nil
}

func (f cronField) value(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < f.min || n > f.max {
			return 0, fmt.Errorf("value %d out of range %d-%d", n, f.min, f.max)
		}
		return n, nil
	}
	if f.names != nil {
		ls := strings.ToLower(s)
		for i, name := range f.names {
			if name == ls {
				return f.min + i, nil
			}
		}
	}
	return 0, fmt.Errorf("invalid value %q", s)
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}

// Next returns the first matching minute strictly after after, in after's
// location, or the zero time if none exists within 8 years.
func (c *Cron) Next(after time.Time) time.Time {
	loc := after.Location()
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc).Add(time.Minute)
	limit := after.AddDate(8, 0, 0)
	for t.Before(limit) {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			t = advance(t, time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc), 24*time.Hour)
		case !c.dayMatches(t):
			t = advance(t, time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc), time.Hour)
		case c.hour&(1<<uint(t.Hour())) == 0:
			t = advance(t, time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc), time.Minute)
		case c.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// advance returns next, or t+fallback if next does not move time forward
// (possible around DST transitions).
func advance(t, next time.Time, fallback time.Duration) time.Time {
	if next.After(t) {
		return next
	}
	return t.Add(fallback)
}
