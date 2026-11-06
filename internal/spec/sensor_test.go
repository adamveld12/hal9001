package spec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const requirementsSensor = `# sensors/github-notifications.yaml
id: github-notifications
interval: 2m              # or cron: "*/5 9-18 * * 1-5"
lang: bash
script: |
  gh api notifications --paginate -q '.[]' 
key: "{{.id}}:{{.updated_at}}"   # Go text/template over the item; configurable re-trigger
first_run: baseline       # baseline = mark all seen without firing; fire = emit everything
timeout: 60s
`

func TestRequirementsSensorLoadsClean(t *testing.T) {
	s, ds := CheckSensor("github-notifications", []byte(requirementsSensor))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	sched, err := s.Schedule()
	if err != nil || sched.Interval != 2*time.Minute {
		t.Fatalf("schedule = %+v, %v", sched, err)
	}
}

func TestSensorDefaults(t *testing.T) {
	s, _ := ParseSensor([]byte("id: s\ninterval: 1m\nlang: jq\nscript: .\nkey: '{{.id}}'\n"))
	if s.Timeout.Std() != time.Minute || s.FirstRun != FirstRunBaseline {
		t.Errorf("timeout %v first_run %q", s.Timeout, s.FirstRun)
	}
}

func TestSensorWithoutScheduleIsManual(t *testing.T) {
	s, ds := CheckSensor("s", []byte("id: s\nlang: bash\nscript: echo\nkey: '{{.id}}'\n"))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	sched, err := s.Schedule()
	if err != nil || !sched.Manual() {
		t.Fatalf("schedule = %+v, %v; want manual", sched, err)
	}
	timed := Sensor{Interval: Duration(time.Minute)}
	if sched, _ := timed.Schedule(); sched.Manual() {
		t.Errorf("an interval sensor must not be manual")
	}
}

func TestSensorValidationErrors(t *testing.T) {
	const ok = "lang: bash\nscript: echo\nkey: '{{.id}}'\n"
	tests := []struct {
		name, yaml, code, path, msg string
	}{
		{"both interval and cron", "id: s\ninterval: 1m\ncron: '* * * * *'\n" + ok, CodeInvalidSchedule, "cron", "not both"},
		{"interval needs a unit", "id: s\ninterval: 30\n" + ok, CodeYAMLType, "interval", `invalid duration "30"`},
		{"interval below one second", "id: s\ninterval: 500ms\n" + ok, CodeInvalidSchedule, "interval", "below the 1s minimum"},
		{"cron field count", "id: s\ncron: '* * * *'\n" + ok, CodeInvalidSchedule, "cron", "want 5 fields"},
		{"cron value range", "id: s\ncron: '61 * * * *'\n" + ok, CodeInvalidSchedule, "cron", "minute"},
		{"cron impossible date", "id: s\ncron: '0 0 31 2 *'\n" + ok, CodeInvalidSchedule, "cron", "never matches"},
		{"key is required", "id: s\ninterval: 1m\nlang: bash\nscript: echo\n", CodeRequired, "key", "key is required"},
		{"key template syntax", "id: s\ninterval: 1m\nlang: bash\nscript: echo\nkey: '{{.id'\n", CodeInvalidTemplate, "key", "unclosed action"},
		{"first_run enum", "id: s\ninterval: 1m\nfirst_run: maybe\n" + ok, CodeInvalidEnum, "first_run", `first_run "maybe" is not valid (one of baseline | fire)`},
		{"lang enum", "id: s\ninterval: 1m\nlang: ruby\nscript: x\nkey: k\n", CodeInvalidEnum, "lang", `lang "ruby" is not valid`},
		{"script required", "id: s\ninterval: 1m\nlang: python\nkey: k\n", CodeRequired, "script", "script is required when lang is python"},
		{"id must match file name", "id: other\ninterval: 1m\n" + ok, CodeIDMismatch, "id", `does not match the file name "s"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckSensor("s", []byte(tt.yaml))
			var found bool
			for _, d := range ds {
				if d.Code == tt.code && d.Path == tt.path && strings.Contains(d.Message, tt.msg) && d.File == "sensors/s.yaml" {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s at %s containing %q in %v", tt.code, tt.path, tt.msg, ds)
			}
		})
	}
}

func TestCronNext(t *testing.T) {
	utc := time.UTC
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, utc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	tests := []struct {
		expr, after, want string
	}{
		{"*/5 9-18 * * 1-5", "2026-11-06 09:00", "2026-11-06 09:05"}, // Friday
		{"*/5 9-18 * * 1-5", "2026-11-06 18:55", "2026-11-09 09:00"}, // Friday evening -> Monday
		{"*/5 9-18 * * 1-5", "2026-11-05 08:00", "2026-11-05 09:00"}, // before the window
		{"30 2 * * *", "2026-11-05 02:30", "2026-11-06 02:30"},       // strictly after
		{"0 0 29 2 *", "2026-11-05 00:00", "2028-02-29 00:00"},       // leap day
		{"0 12 15 * mon", "2026-11-05 00:00", "2026-11-09 12:00"},    // dom OR dow when both restricted
		{"0 12 1 jan,jul *", "2026-11-05 00:00", "2027-01-01 12:00"}, // month names and lists
		{"15,45 * * * 7", "2026-11-08 10:20", "2026-11-08 10:45"},    // 7 is Sunday
		{"10-20/5 0 * * *", "2026-11-05 00:10", "2026-11-05 00:15"},  // range with step
		{"5/20 0 * * *", "2026-11-05 00:06", "2026-11-05 00:25"},     // N/step runs to the maximum
	}
	for _, tt := range tests {
		c, err := ParseCron(tt.expr)
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		if got := c.Next(at(tt.after)); !got.Equal(at(tt.want)) {
			t.Errorf("%q after %s = %s, want %s", tt.expr, tt.after, got.Format("2006-01-02 15:04"), tt.want)
		}
	}
}

func TestCronRejects(t *testing.T) {
	for expr, want := range map[string]string{
		"":               "want 5 fields",
		"* * * * * *":    "want 5 fields",
		"*/0 * * * *":    "invalid step",
		"5-1 * * * *":    "backwards",
		"* 24 * * *":     "hour",
		"* * 0 * *":      "day-of-month",
		"* * * 13 *":     "month",
		"* * * * funday": "invalid value",
		"1,,2 * * * *":   "empty list element",
	} {
		_, err := ParseCron(expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseCron(%q) error = %v, want containing %q", expr, err, want)
		}
	}
}

func TestIntervalScheduleNext(t *testing.T) {
	s := Sensor{Interval: Duration(2 * time.Minute)}
	sched, err := s.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if got := sched.Next(base); !got.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("Next = %v", got)
	}
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90s": 90 * time.Second, "20m": 20 * time.Minute, "1h30m": 90 * time.Minute,
		"7d": 7 * 24 * time.Hour, "1d12h": 36 * time.Hour, "0.5d": 12 * time.Hour,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "30", "abc", "5x"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) should fail", in)
		}
	}
	for d, want := range map[time.Duration]string{
		time.Hour: "1h", 30 * time.Minute: "30m", 90 * time.Second: "1m30s", 7 * 24 * time.Hour: "7d", 36 * time.Hour: "36h", 60 * time.Second: "1m",
	} {
		if got := Duration(d).String(); got != want {
			t.Errorf("Duration(%v).String() = %q, want %q", d, got, want)
		}
		if back, _ := ParseDuration(Duration(d).String()); back != d {
			t.Errorf("%q does not parse back to %v", Duration(d).String(), d)
		}
	}
	var viaJSON struct{ T Duration }
	if err := json.Unmarshal([]byte(`{"T":"20m"}`), &viaJSON); err != nil || viaJSON.T.Std() != 20*time.Minute {
		t.Errorf("JSON duration: %v %v", viaJSON, err)
	}
	if got := mustJSON(t, viaJSON); got != `{"T":"20m"}` {
		t.Errorf("JSON out = %s", got)
	}
}

func TestSensorMarshalRoundTrip(t *testing.T) {
	s, _ := ParseSensor([]byte(requirementsSensor))
	out, err := MarshalSensor(*s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "first_run") || strings.Contains(string(out), "timeout") {
		t.Errorf("defaults written:\n%s", out)
	}
	back, ds := ParseSensor(out)
	if len(ds) != 0 || mustJSON(t, back) != mustJSON(t, s) {
		t.Errorf("round trip: %v\n%s", ds, out)
	}
}
