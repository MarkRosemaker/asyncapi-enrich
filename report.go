package enrich

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Report says what a recording actually observed.
//
// It exists because the interesting outcome of a recording is often the thing
// that did not happen. "Sixty seconds, four trades, never a ping" is what tells
// you the ping in the documentation is not one this feed sends — and that is a
// fact about the API, arrived at by observation, which is the whole point.
type Report struct {
	// Sessions holds one entry per session, in the order they were recorded.
	Sessions []*SessionReport
}

// SessionReport says what one session observed.
type SessionReport struct {
	// Received is how many frames came back.
	Received int
	// Seen counts the frames of each kind, by the value of the discriminator.
	// It is nil when the session named no discriminator.
	Seen map[string]int
	// Short is how many fewer frames came back than the session asked for.
	Short int
	// Missing maps each kind that came up short to how many more were wanted.
	Missing map[string]int
}

// newSessionReport compares what was asked for against what arrived.
func newSessionReport(until *Until, count int, seen map[string]int) *SessionReport {
	sr := &SessionReport{Received: count}

	if until.Discriminator != "" {
		sr.Seen = seen
	}

	if count < until.Messages {
		sr.Short = until.Messages - count
	}

	for kind, want := range until.Kinds {
		if got := seen[kind]; got < want {
			if sr.Missing == nil {
				sr.Missing = map[string]int{}
			}

			sr.Missing[kind] = want - got
		}
	}

	return sr
}

// Complete reports whether every session met every condition it declared.
func (r *Report) Complete() bool {
	for _, sr := range r.Sessions {
		if !sr.Complete() {
			return false
		}
	}

	return true
}

// Complete reports whether the session met every condition it declared.
func (sr *SessionReport) Complete() bool {
	return sr.Short == 0 && len(sr.Missing) == 0
}

// String summarises the recording in the form go generate should print.
func (r *Report) String() string {
	lines := make([]string, 0, len(r.Sessions))

	for i, sr := range r.Sessions {
		lines = append(lines, fmt.Sprintf("sessions[%d]: %s", i, sr))
	}

	return strings.Join(lines, "\n")
}

// String summarises what the session observed.
func (sr *SessionReport) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%d received", sr.Received)

	if len(sr.Seen) > 0 {
		parts := make([]string, 0, len(sr.Seen))

		for _, kind := range slices.Sorted(maps.Keys(sr.Seen)) {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, sr.Seen[kind]))
		}

		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}

	if sr.Complete() {
		return b.String()
	}

	if sr.Short > 0 {
		fmt.Fprintf(&b, "; %d short", sr.Short)
	}

	for _, kind := range slices.Sorted(maps.Keys(sr.Missing)) {
		fmt.Fprintf(&b, "; never saw %d more %s", sr.Missing[kind], kind)
	}

	return b.String()
}
