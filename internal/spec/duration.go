package spec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration is a time.Duration that reads and writes as a string such as
// "90s", "20m", "1h30m" or "7d". The d suffix (24h) is accepted in addition to
// the units of time.ParseDuration. A bare number without a unit is rejected.
type Duration time.Duration

var daysRE = regexp.MustCompile(`(\d+(?:\.\d+)?)d`)

// ParseDuration parses s like time.ParseDuration, additionally accepting d.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	s = daysRE.ReplaceAllStringFunc(s, func(m string) string {
		days, err := strconv.ParseFloat(strings.TrimSuffix(m, "d"), 64)
		if err != nil {
			return m
		}
		return strconv.FormatFloat(days*24, 'f', -1, 64) + "h"
	})
	return time.ParseDuration(s)
}

// String formats d compactly: 30m, 1h, 7d, 1h30m, 90s -> 1m30s.
func (d Duration) String() string {
	td := time.Duration(d)
	if td != 0 && td%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(td/(24*time.Hour)), 10) + "d"
	}
	s := td.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalText implements encoding.TextMarshaler (JSON and YAML output).
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler (JSON input).
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}

// UnmarshalYAML implements yaml.Unmarshaler. Errors are reported as a
// *yaml.TypeError so that decoding of the rest of the document continues.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: duration must be a string such as 90s, 20m or 7d", n.Line)}}
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: invalid duration %q (use a unit such as 90s, 20m, 1h, 7d)", n.Line, n.Value)}}
	}
	*d = Duration(v)
	return nil
}
