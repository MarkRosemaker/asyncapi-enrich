package enrich

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait bounds how long a control frame — the closing handshake's Close
	// frame, in particular — may take to write.
	writeWait = 5 * time.Second
	// closeGracePeriod bounds how long Session waits for the server's half of
	// the WebSocket closing handshake (RFC 6455 §7.1.1) after sending its own
	// Close frame. The handshake is attempted either way; this only bounds the
	// wait for it.
	closeGracePeriod = 2 * time.Second
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
	// Save, if set, is called after every frame a session records — a send, a
	// receive, or the closing unsubscribe — so that a crash mid-recording loses
	// at most the frame in flight, not the whole session. It is given every
	// session, not just the one that changed, since they share one file.
	//
	// Sessions record concurrently, so Save may be called from several
	// goroutines; Recorder serialises those calls itself, so Save need not be
	// safe for concurrent use on its own.
	Save func(Sessions) error
}

// Record plays every session that has not already met the stop condition, and
// fills in the frames that came back. Sessions record concurrently — recording
// several costs no more wall-clock time than recording one.
//
// A session whose existing frames already satisfy [Recorder.Until] is left
// alone and reported as skipped: rerunning a recording that already succeeded
// dials nothing and returns at once. A session that falls short — a stricter
// -messages than a previous run found, say — is recorded from scratch: what it
// already had came from a different connection with its own clock, so it
// cannot simply be extended.
//
// Record returns the first error that stopped a session from being recorded. A
// stop condition that was not met is not one of those: a feed that never sent
// the message you were waiting for is an answer about the feed, and is reported
// in the returned [Report] instead.
func (r *Recorder) Record(ctx context.Context, ss Sessions) (*Report, error) {
	if err := ss.Validate(); err != nil {
		return nil, err
	}

	if r.Until == nil {
		return nil, ErrNoTimeout
	}

	if err := r.Until.Validate(); err != nil {
		return nil, err
	}

	var mu sync.Mutex // guards every mutation of ss and every call to r.Save

	save := func() error {
		if r.Save == nil {
			return nil
		}

		return r.Save(ss)
	}

	reports := make([]*SessionReport, len(ss))
	errs := make([]error, len(ss))

	var wg sync.WaitGroup

	for i, s := range ss {
		wg.Add(1)

		go func() {
			defer wg.Done()

			reports[i], errs[i] = r.session(ctx, s, &mu, save)
		}()
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
	}

	return &Report{Sessions: reports}, nil
}

// Session plays one session on its own, without the concurrency or incremental
// saving [Recorder.Record] provides for a whole file — see that method for the
// form actually meant for recording.
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

	var mu sync.Mutex

	return r.session(ctx, s, &mu, func() error { return nil })
}

// session (re)records one session. It holds mu while it mutates s or calls
// save, since both may happen concurrently with another session doing the same
// — they share one Sessions slice and one file.
func (r *Recorder) session(ctx context.Context, s *Session, mu *sync.Mutex, save func() error) (*SessionReport, error) {
	mu.Lock()
	count, seen := countReceived(s.Frames, r.Until.Discriminator)
	mu.Unlock()

	if r.Until.satisfied(count, seen) {
		sr := newSessionReport(r.Until, count, seen)
		sr.Skipped = true

		return sr, nil
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
	defer conn.Close() //nolint:errcheck

	// commit masks, appends, and saves one frame, atomically with respect to
	// every other session sharing this file.
	commit := func(f *Frame) error {
		if err := mask.Frame(f); err != nil {
			return err
		}

		mu.Lock()
		s.Frames = append(s.Frames, f)
		err := save()
		mu.Unlock()

		return err
	}

	// What this session already had cannot simply be extended into what is
	// wanted now: it came from a different connection, with its own clock, so
	// it is discarded and recording starts over. Only the authored send frames
	// are kept — they carry no "at" and are exactly what was written, so
	// keeping them is not a capture to redo, just data to reuse.
	mu.Lock()
	s.Frames = s.sends()

	for _, f := range s.Frames {
		if err := mask.Frame(f); err != nil {
			mu.Unlock()

			return nil, err
		}
	}

	s.URI = mask.MaskURI(s.URI)
	saveErr := save()
	mu.Unlock()

	if saveErr != nil {
		return nil, saveErr
	}

	for _, f := range s.Frames {
		if err := conn.WriteMessage(websocket.TextMessage, f.Send); err != nil {
			return nil, fmt.Errorf("sending: %w", err)
		}
	}

	sr, err := r.listen(ctx, conn, now(), now, commit)
	if err != nil {
		return nil, err
	}

	r.close(conn, s, commit)

	return sr, nil
}

// listen reads frames until the stop condition is met or the context is done,
// committing each one as it arrives.
func (r *Recorder) listen(
	ctx context.Context, conn *websocket.Conn, start time.Time, now func() time.Time,
	commit func(*Frame) error,
) (*SessionReport, error) {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(dl); err != nil {
			return nil, fmt.Errorf("setting the read deadline: %w", err)
		}
	}

	var (
		until          = r.Until
		count, notJSON int
		seen           = map[string]int{}
	)

	for !until.satisfied(count, seen) {
		if err := ctx.Err(); err != nil {
			break
		}

		tp, data, err := conn.ReadMessage()
		if err != nil {
			// A timeout is how a quiet feed ends a recording, not a failure.
			if isTimeout(err) || ctx.Err() != nil {
				break
			}

			return nil, fmt.Errorf("reading: %w", err)
		}

		v, ok := payload(tp, data)
		if !ok {
			notJSON++
		}

		if err := commit(&Frame{At: NewDuration(now().Sub(start)), Receive: v}); err != nil {
			return nil, err
		}

		count++

		if until.Discriminator != "" {
			if kind, ok := discriminate(v, until.Discriminator); ok {
				seen[kind]++
			}
		}
	}

	sr := newSessionReport(until, count, seen)
	sr.NotJSON = notJSON

	return sr, nil
}

// close performs the WebSocket closing handshake (RFC 6455 §7.1.1) instead of
// just cutting the TCP connection, sending the session's Unsubscribe frame
// first if it has one. This runs whether the stop condition was met or the
// timeout ran out — it is the natural end of a session either way, not
// something worth skipping because time ran short.
func (r *Recorder) close(conn *websocket.Conn, s *Session, commit func(*Frame) error) {
	if len(s.Unsubscribe) > 0 {
		if err := conn.WriteMessage(websocket.TextMessage, s.Unsubscribe); err == nil {
			_ = commit(&Frame{Send: s.Unsubscribe})
		}
	}

	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(writeWait))

	// Wait, bounded, for the server's half of the handshake; discard whatever
	// arrives in the meantime.
	_ = conn.SetReadDeadline(time.Now().Add(closeGracePeriod))

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// countReceived tallies a session's already-recorded receive frames the same
// way listen does, so a rerun can tell whether it needs to dial at all.
func countReceived(frames []*Frame, discriminator string) (count int, seen map[string]int) {
	seen = map[string]int{}

	for _, f := range frames {
		if len(f.Receive) == 0 {
			continue
		}

		count++

		if discriminator != "" {
			if kind, ok := discriminate(f.Receive, discriminator); ok {
				seen[kind]++
			}
		}
	}

	return count, seen
}

// payload turns a frame off the wire into something a sessions file can hold,
// and reports whether it was JSON to begin with.
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
