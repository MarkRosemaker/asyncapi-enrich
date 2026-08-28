package enrich

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// Recorder plays sessions against real servers and fills in what comes back.
type Recorder struct {
	// REQUIRED. Until says when each session stops listening.
	Until *Until
	// Mask is applied to every URI and frame before it is kept. A nil Mask means
	// the default masker, not no masking — there is no way to ask for none,
	// because there is no good reason to want one.
	Mask *Masker
	// Dialer connects to the server. The zero value means
	// [websocket.DefaultDialer].
	Dialer *websocket.Dialer
	// Now returns the current time. The zero value means [time.Now]. Tests set it.
	Now func() time.Time
}

// Record plays every session and fills in the frames that came back.
//
// It returns the first error that stopped a session from being recorded. A stop
// condition that was not met is not one of those: a feed that never sent the
// message you were waiting for is an answer about the feed, and is reported in
// the returned [Report] instead.
func (r *Recorder) Record(ctx context.Context, ss Sessions) (*Report, error) {
	if err := ss.Validate(); err != nil {
		return nil, err
	}

	rep := &Report{Sessions: make([]*SessionReport, 0, len(ss))}

	for i, s := range ss {
		sr, err := r.Session(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}

		rep.Sessions = append(rep.Sessions, sr)
	}

	return rep, nil
}

// Session plays one session and replaces its frames with what actually crossed
// the wire: the frames that were sent, in order, followed by the ones that came
// back, each stamped with how long after connecting it arrived.
//
// The session's URI is written back exactly as it was authored, so a reference
// to an environment variable survives and a literal credential does not.
func (r *Recorder) Session(ctx context.Context, s *Session) (*SessionReport, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	if r.Until == nil {
		return nil, ErrNoTimeout
	}

	if err := r.Until.Validate(); err != nil {
		return nil, err
	}

	u, err := s.dial()
	if err != nil {
		return nil, err
	}

	mask := r.Mask
	if mask == nil {
		mask = NewMasker()
	}

	now := r.Now
	if now == nil {
		now = time.Now
	}

	ctx, cancel := context.WithTimeout(ctx, r.Until.Timeout)
	defer cancel()

	dialer := r.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		// The URI is masked before it reaches an error a caller might log.
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

	received, sr, err := r.listen(ctx, conn, start, now)
	if err != nil {
		return nil, err
	}

	s.Frames = append(frames, received...)

	if err := mask.Session(s); err != nil {
		return nil, fmt.Errorf("masking: %w", err)
	}

	return sr, nil
}

// listen reads frames until the stop condition is met or the context is done.
func (r *Recorder) listen(
	ctx context.Context, conn *websocket.Conn, start time.Time, now func() time.Time,
) ([]*Frame, *SessionReport, error) {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(dl); err != nil {
			return nil, nil, fmt.Errorf("setting the read deadline: %w", err)
		}
	}

	var (
		frames   = []*Frame{}
		seen     = map[string]int{}
		notJSON  int
		until    = r.Until
		discrim  = until.Discriminator
		maxFrame = func() bool { return until.satisfied(len(frames), seen) }
	)

	for !maxFrame() {
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

		v, ok := payload(tp, data)
		if !ok {
			notJSON++
		}

		frames = append(frames, &Frame{At: NewDuration(now().Sub(start)), Receive: v})

		if discrim != "" {
			if kind, ok := discriminate(v, discrim); ok {
				seen[kind]++
			}
		}
	}

	sr := newSessionReport(until, len(frames), seen)
	sr.NotJSON = notJSON

	return frames, sr, nil
}

// payload turns a frame off the wire into something an interactions file can
// hold, and reports whether it was JSON to begin with.
//
// A text frame that is JSON is kept as it came. Anything else — plain text, or
// the base64 of a binary frame — is kept as a JSON string. Nothing is dropped:
// a feed that does not answer in JSON is still a feed worth recording, and
// guessing at an encoding here would put invention into the evidence.
func payload(messageType int, data []byte) (jsontext.Value, bool) {
	if messageType == websocket.TextMessage {
		v := jsontext.Value(data)
		if err := v.Compact(); err == nil {
			return v, true
		}

		return quote(string(data)), false
	}

	return quote(base64.StdEncoding.EncodeToString(data)), false
}

// quote returns s as a JSON string.
func quote(s string) jsontext.Value {
	b, err := json.Marshal(s)
	if err != nil {
		// A Go string always marshals.
		panic(err)
	}

	return b
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
