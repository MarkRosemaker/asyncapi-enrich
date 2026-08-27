package enrich

import (
	"time"
)

// Duration is a [time.Duration] that reads and writes as a string, e.g. "1.5s",
// so that a hand-authored interactions file says "60s" rather than 60000000000.
//
// It keeps the text it was read from. A file that says "60s" still says "60s"
// after a recording rewrites it, rather than drifting to "1m0s" — a recording
// should show up as a diff of the frames that arrived and nothing else.
type Duration struct {
	d   time.Duration
	raw string
}

// NewDuration returns a Duration of d.
func NewDuration(d time.Duration) Duration { return Duration{d: d} }

// Duration returns the duration.
func (d Duration) Duration() time.Duration { return d.d }

// IsZero reports whether the duration is zero, which is what omitzero asks.
func (d Duration) IsZero() bool { return d.d == 0 }

// String returns the text the duration was read from, or the form
// [time.Duration.String] gives it if it was not read from text.
func (d Duration) String() string {
	if d.raw != "" {
		return d.raw
	}

	return d.d.String()
}

// MarshalText implements [encoding.TextMarshaler].
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText implements [encoding.TextUnmarshaler].
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}

	d.d, d.raw = v, string(b)

	return nil
}
