package enrich

import (
	"errors"
	"time"
)

// Until says when a recording stops.
//
// It is not part of a session, because it is not part of the API: it is what you
// asked the recorder for, not something the server did. An async API does not
// end a session the way a response ends an HTTP request, so the condition has to
// be stated somewhere, and it belongs with the recorder rather than in a file
// that describes a connection.
//
// Recording stops as soon as every condition that was set is satisfied, or when
// Timeout expires — whichever comes first.
//
// A timeout that expires with conditions unmet is not a failure. It is the
// useful answer to "does this feed ever send a ping?", and is reported in the
// [SessionReport] so that the gap is visible rather than silent.
type Until struct {
	// REQUIRED. Timeout is how long to listen before giving up.
	Timeout time.Duration
	// Messages is the total number of received frames to wait for.
	Messages int
	// Discriminator is the name of the top-level field that says what kind of
	// message a received frame is, e.g. "type". Required when Kinds is set.
	Discriminator string
	// Kinds maps a value of the discriminator field to the number of frames of
	// that kind to wait for, e.g. {"trade": 3, "ping": 1}.
	//
	// This is the condition worth reaching for: a specification is only complete
	// once every kind of message has actually been seen, and a generated reader
	// only has to discriminate when there is more than one kind to tell apart.
	Kinds map[string]int
}

var (
	// ErrNoTimeout is returned for a stop condition with no timeout. Without one
	// a recording of a quiet feed would never return.
	ErrNoTimeout = errors.New("must set a timeout")
	// ErrNoDiscriminator is returned when kinds are counted but no field is named
	// to tell one kind from another.
	ErrNoDiscriminator = errors.New("must set a discriminator when kinds are set")
)

// Validate checks that the stop condition can be evaluated.
func (u *Until) Validate() error {
	if u.Timeout <= 0 {
		return ErrNoTimeout
	}

	if len(u.Kinds) > 0 && u.Discriminator == "" {
		return ErrNoDiscriminator
	}

	return nil
}

// satisfied reports whether every condition that was set has been met.
func (u *Until) satisfied(count int, seen map[string]int) bool {
	if u.Messages == 0 && len(u.Kinds) == 0 {
		// Nothing was asked for beyond the timeout, so only the timeout ends it.
		return false
	}

	if count < u.Messages {
		return false
	}

	for kind, want := range u.Kinds {
		if seen[kind] < want {
			return false
		}
	}

	return true
}
