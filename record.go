package enrich

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// ErrNoServer is returned when a session names a server the recorder was not
// given a URL for.
var ErrNoServer = errors.New("no URL for server")

// Recorder plays sessions against real servers and fills in what comes back.
type Recorder struct {
	// REQUIRED. URLs maps the server key a session names to the URL to dial.
	//
	// The caller builds these, which is where a credential is read from the
	// environment and put into the URL. It goes no further than the dial: what is
	// written back is the session, which names the server by key.
	URLs map[string]string
	// Mask is applied to every frame before it is kept. A nil Mask means the
	// default masker, not no masking — there is no way to ask for none, because
	// there is no good reason to want one.
	Mask *Masker
	// Dialer connects to the server. The zero value means
	// [websocket.DefaultDialer].
	Dialer *websocket.Dialer
	// Now returns the current time. The zero value means [time.Now]. Tests set it.
	Now func() time.Time
}

// Record plays every session of ixs and fills in the frames that came back.
//
// It returns the first error that stopped a session from being recorded. A stop
// condition that was not met is not one of those: a feed that never sent the
// message you were waiting for is an answer about the feed, and is reported in
// the returned [Report] instead.
func (r *Recorder) Record(ctx context.Context, ixs *Interactions) (*Report, error) {
	if err := ixs.Validate(); err != nil {
		return nil, err
	}

	rep := &Report{Sessions: make([]*SessionReport, 0, len(ixs.Sessions))}

	for i, s := range ixs.Sessions {
		sr, err := r.Session(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("sessions[%d]: %w", i, err)
		}

		rep.Sessions = append(rep.Sessions, sr)
	}

	return rep, nil
}

// Session plays one session and replaces its frames with what actually crossed
// the wire: the frames that were sent, in order, interleaved with the ones that
// came back, each stamped with how long after connecting it arrived.
func (r *Recorder) Session(ctx context.Context, s *Session) (*SessionReport, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	rawURL, ok := r.URLs[s.Server]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoServer, s.Server)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing the URL of server %q: %w", s.Server, err)
	}

	mask := r.Mask
	if mask == nil {
		mask = NewMasker()
	}

	now := r.Now
	if now == nil {
		now = time.Now
	}

	ctx, cancel := context.WithTimeout(ctx, s.Until.Timeout.Duration())
	defer cancel()

	dialer := r.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		// The URL is masked before it reaches an error a caller might log.
		return nil, fmt.Errorf("dialing %s: %w", mask.URL(u), err)
	}
	defer conn.Close()

	start := now()
	sent := s.sends()
	frames := make([]*Frame, 0, len(sent))

	for _, f := range sent {
		if err := conn.WriteMessage(websocket.TextMessage, f.Send); err != nil {
			return nil, fmt.Errorf("sending: %w", err)
		}

		frames = append(frames, &Frame{
			At:   NewDuration(now().Sub(start)),
			Send: f.Send,
		})
	}

	received, sr, err := r.listen(ctx, conn, s.Until, start, now)
	if err != nil {
		return nil, err
	}

	s.Frames = append(frames, received...)
	s.RecordedAt = start.UTC().Truncate(time.Second)

	if err := mask.Session(s); err != nil {
		return nil, fmt.Errorf("masking: %w", err)
	}

	return sr, nil
}

// listen reads frames until the stop condition is met or the context is done.
func (r *Recorder) listen(
	ctx context.Context, conn *websocket.Conn, until *Until,
	start time.Time, now func() time.Time,
) ([]*Frame, *SessionReport, error) {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(dl); err != nil {
			return nil, nil, fmt.Errorf("setting the read deadline: %w", err)
		}
	}

	frames := []*Frame{}
	seen := map[string]int{}

	for !satisfied(until, len(frames), seen) {
		if err := ctx.Err(); err != nil {
			break
		}

		tp, data, err := conn.ReadMessage()
		if err != nil {
			// A timeout is how a quiet feed ends a recording, not a failure.
			if isTimeout(err) || ctx.Err() != nil {
				break
			}

			return nil, nil, fmt.Errorf("reading: %w", err)
		}

		if tp != websocket.TextMessage {
			// Binary frames are a different problem — a payload that is not JSON
			// needs a schema format that says what it is instead.
			return nil, nil, fmt.Errorf("received a message of type %d, want text", tp)
		}

		v := jsontext.Value(data)
		if err := v.Compact(); err != nil {
			return nil, nil, fmt.Errorf("received a message that is not JSON: %w", err)
		}

		frames = append(frames, &Frame{At: NewDuration(now().Sub(start)), Receive: v})

		if until.Discriminator != "" {
			if kind, ok := discriminate(v, until.Discriminator); ok {
				seen[kind]++
			}
		}
	}

	return frames, newSessionReport(until, len(frames), seen), nil
}

// satisfied reports whether every condition that was set has been met.
func satisfied(until *Until, count int, seen map[string]int) bool {
	if until.Messages == 0 && len(until.Kinds) == 0 {
		// Nothing was asked for beyond the timeout, so only the timeout ends it.
		return false
	}

	if count < until.Messages {
		return false
	}

	for kind, want := range until.Kinds {
		if seen[kind] < want {
			return false
		}
	}

	return true
}

// discriminate returns the value of the named top-level field of a payload, if
// it has one and it is a string.
func discriminate(v jsontext.Value, field string) (string, bool) {
	var msg map[string]jsontext.Value
	if err := json.Unmarshal(v, &msg); err != nil {
		return "", false
	}

	raw, ok := msg[field]
	if !ok {
		return "", false
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}

	return s, true
}

// isTimeout reports whether an error is the read deadline expiring.
func isTimeout(err error) bool {
	var t interface{ Timeout() bool }

	return errors.As(err, &t) && t.Timeout()
}
