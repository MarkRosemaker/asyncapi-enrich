package enrich

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"time"
)

// Interactions is the recorded traffic of an async API, the counterpart of
// openapi-enrich's interactions file. It lives next to the specification it
// enriches, e.g. api/interactions.json beside api/asyncapi.json.
type Interactions struct {
	// Sessions are the connections that were, or are to be, recorded.
	Sessions []*Session `json:"sessions"`
}

// Session is one connection to one server, and every frame that crossed it.
//
// You author Server, Until, and the frames you want sent. Recording fills in the
// frames that came back, and the time each of them arrived.
type Session struct {
	// REQUIRED. Server is the key of the server to connect to, as it appears in the
	// servers object of the AsyncAPI document.
	//
	// It is a key and not a URL on purpose: the URL that is actually dialled carries
	// the credentials, and a key cannot leak them into a file that gets committed.
	Server string `json:"server"`
	// A short description of what this session is meant to observe.
	Description string `json:"description,omitzero"`
	// REQUIRED. Until says when to stop listening.
	Until *Until `json:"until"`
	// Frames are the messages of this session, in the order they crossed the wire.
	Frames []*Frame `json:"frames"`
	// RecordedAt is when this session was last recorded. It is set by [Record].
	RecordedAt time.Time `json:"recordedAt,omitzero"`
}

// Frame is a single message, in one direction.
//
// Exactly one of Send and Receive is set: a frame is either something the
// application wrote or something it read, never both.
type Frame struct {
	// At is how long after the connection opened this frame crossed the wire.
	// It is set by [Record], and is what makes a heartbeat interval discoverable.
	At Duration `json:"at,omitzero"`
	// Send is the payload the application writes. You author these.
	Send jsontext.Value `json:"send,omitempty"`
	// Receive is the payload the application reads. Recording appends these.
	Receive jsontext.Value `json:"receive,omitempty"`
}

// Until declares when a recording stops.
//
// An async API does not end a session the way a response ends an HTTP request, so
// the condition has to be stated. Recording stops as soon as every condition that
// was set is satisfied, or when Timeout expires — whichever comes first.
//
// A timeout that expires with conditions unmet is not a failure. It is the useful
// answer to "does this feed ever send a ping?", and [Record] reports which
// conditions went unsatisfied so that the gap is visible rather than silent.
type Until struct {
	// REQUIRED. Timeout is how long to listen before giving up.
	Timeout Duration `json:"timeout"`
	// Messages is the total number of received frames to wait for.
	Messages int `json:"messages,omitzero"`
	// Discriminator is the name of the top-level field that says what kind of
	// message a received frame is, e.g. "type". Required when Kinds is set.
	Discriminator string `json:"discriminator,omitzero"`
	// Kinds maps a value of the discriminator field to the number of frames of
	// that kind to wait for, e.g. {"trade": 3, "ping": 1}.
	//
	// This is the condition worth reaching for: a specification is only complete
	// once every kind of message has actually been seen, and a generated reader
	// only has to discriminate when there is more than one kind to tell apart.
	Kinds map[string]int `json:"kinds,omitempty"`
}

var (
	// ErrNoTimeout is returned for a session whose stop condition has no timeout.
	// Without one a recording of a quiet feed would never return.
	ErrNoTimeout = errors.New("must set a timeout")
	// ErrNoDiscriminator is returned when kinds are counted but no field is named
	// to tell one kind from another.
	ErrNoDiscriminator = errors.New("must set a discriminator when kinds are set")
	// ErrBothDirections is returned for a frame that is both sent and received.
	ErrBothDirections = errors.New("must not set both send and receive")
	// ErrNoDirection is returned for a frame that is neither sent nor received.
	ErrNoDirection = errors.New("must set either send or receive")
)

// Validate checks that the interactions can be recorded.
func (ixs *Interactions) Validate() error {
	for i, s := range ixs.Sessions {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("sessions[%d]: %w", i, err)
		}
	}

	return nil
}

// Validate checks that the session can be recorded.
func (s *Session) Validate() error {
	if s.Server == "" {
		return errors.New("server is required")
	}

	if s.Until == nil {
		return errors.New("until is required")
	}

	if err := s.Until.Validate(); err != nil {
		return fmt.Errorf("until: %w", err)
	}

	for i, f := range s.Frames {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("frames[%d]: %w", i, err)
		}
	}

	return nil
}

// Validate checks that the stop condition can be evaluated.
func (u *Until) Validate() error {
	if u.Timeout.Duration() <= 0 {
		return ErrNoTimeout
	}

	if len(u.Kinds) > 0 && u.Discriminator == "" {
		return ErrNoDiscriminator
	}

	return nil
}

// Validate checks that the frame goes in exactly one direction.
func (f *Frame) Validate() error {
	switch {
	case len(f.Send) > 0 && len(f.Receive) > 0:
		return ErrBothDirections
	case len(f.Send) == 0 && len(f.Receive) == 0:
		return ErrNoDirection
	default:
		return nil
	}
}

// sends returns the frames the application writes, in order.
func (s *Session) sends() []*Frame {
	out := make([]*Frame, 0, len(s.Frames))

	for _, f := range s.Frames {
		if len(f.Send) > 0 {
			out = append(out, f)
		}
	}

	return out
}
