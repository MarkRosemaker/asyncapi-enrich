package enrich

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// Sessions is the recorded traffic of an async API, the counterpart of
// openapi-enrich's interactions file. It lives next to the specification it
// enriches, e.g. api/sessions.json beside api/asyncapi.json.
//
// It holds only two things: what is needed to open a connection, and what
// crossed it. Everything else — what the server is called, what the messages
// mean, why the session exists — belongs in the AsyncAPI document, which is the
// artefact this one is here to improve.
type Sessions []*Session

// Session is one connection, and every frame that crossed it.
//
// You author the URI and the frames you want sent. Recording fills in the frames
// that came back and the time each of them arrived.
type Session struct {
	// REQUIRED. URI is the address to dial, e.g.
	// "wss://ws.finnhub.io?token=$FINNHUB_API_KEY".
	//
	// References to environment variables are expanded when dialling and written
	// back unexpanded, so the file stays runnable without ever holding the
	// credential. A URI that carries a literal credential instead is masked on
	// the way out — see [Masker.URL].
	//
	// The URI is what the specification is enriched from: its scheme gives the
	// server's protocol, its host and port give the host, its path gives the
	// channel address, and a credential in its query gives a security scheme of
	// type httpApiKey with `in: query`.
	URI string `json:"uri"`
	// Frames are the messages of this session, in the order they crossed the wire.
	Frames []*Frame `json:"frames"`
	// Unsubscribe, if set, is sent right before the connection closes — the
	// natural end of a session, symmetric to the subscribe frames it opened
	// with. You author this; it is sent once recording stops, whether the stop
	// condition was met or the timeout ran out, as long as the connection is
	// still open. The closing handshake (RFC 6455 §7.1.1) happens either way,
	// with or without one.
	Unsubscribe jsontext.Value `json:"unsubscribe,omitempty"`
}

// Frame is a single message, in one direction.
//
// Exactly one of Send and Receive is set: a frame is either something the
// application wrote or something it read, never both.
type Frame struct {
	// At is how long after the connection opened this frame crossed the wire.
	// It is set by [Recorder.Record], and is what makes a heartbeat interval
	// discoverable.
	At Duration `json:"at,omitzero"`
	// Send is the payload the application writes. You author these.
	Send jsontext.Value `json:"send,omitempty"`
	// Receive is the payload the application reads. Recording appends these.
	//
	// A frame that is not JSON is kept as a JSON string rather than dropped: a
	// feed that answers in base64 or plain text is still a feed worth recording,
	// and what it sent is still the evidence.
	Receive jsontext.Value `json:"receive,omitempty"`
}

var (
	// ErrNoURI is returned for a session with nothing to dial.
	ErrNoURI = errors.New("uri is required")
	// ErrBothDirections is returned for a frame that is both sent and received.
	ErrBothDirections = errors.New("must not set both send and receive")
	// ErrNoDirection is returned for a frame that is neither sent nor received.
	ErrNoDirection = errors.New("must set either send or receive")
)

// Validate checks that every session can be recorded.
func (ss Sessions) Validate() error {
	for i, s := range ss {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}

	return nil
}

// Validate checks that the session can be recorded.
func (s *Session) Validate() error {
	if s.URI == "" {
		return ErrNoURI
	}

	if _, err := url.Parse(os.ExpandEnv(s.URI)); err != nil {
		return fmt.Errorf("uri: %w", err)
	}

	for i, f := range s.Frames {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("frames[%d]: %w", i, err)
		}
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

// dial returns the URI to actually connect to, with environment variables
// expanded. The expanded form is never written back.
func (s *Session) dial() (*url.URL, error) {
	return url.Parse(os.ExpandEnv(s.URI))
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
