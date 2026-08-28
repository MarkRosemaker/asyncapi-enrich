package enrich_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
	"github.com/gorilla/websocket"
)

// stepClock advances by a fixed step on every call, so that the recorded
// arrival times of a test are the same on every run.
func stepClock(step time.Duration) func() time.Time {
	var (
		mu sync.Mutex
		t  = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	)

	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		now := t
		t = t.Add(step)

		return now
	}
}

var upgrader = websocket.Upgrader{}

// serve starts a WebSocket server that hands each connection to fn, and returns
// the ws:// URL to dial it.
func serve(t *testing.T, fn func(*websocket.Conn)) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrading: %v", err)

			return
		}
		defer conn.Close() //nolint:errcheck

		fn(conn)
	}))
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// writeAll sends each message as a text frame.
func writeAll(t *testing.T, conn *websocket.Conn, msgs ...string) {
	t.Helper()

	for _, msg := range msgs {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Errorf("writing: %v", err)

			return
		}
	}
}

func TestRecordSession(t *testing.T) {
	var got string

	uri := serve(t, func(conn *websocket.Conn) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("reading: %v", err)

			return
		}

		got = string(data)

		writeAll(t, conn,
			`{"type":"trade","data":[{"s":"AAPL","p":190.5}]}`,
			`{"type":"ping"}`,
			`{"type":"trade","data":[{"s":"AAPL","p":190.6}]}`,
		)
	})

	s := &enrich.Session{
		URI: uri,
		Frames: []*enrich.Frame{
			{Send: jsontext.Value(`{"type":"subscribe","symbol":"AAPL"}`)},
		},
	}

	r := &enrich.Recorder{
		Until: &enrich.Until{
			Timeout:       10 * time.Second,
			Discriminator: "type",
			Kinds:         map[string]int{"trade": 2, "ping": 1},
		},
		Now: stepClock(100 * time.Millisecond),
	}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if want := `{"type":"subscribe","symbol":"AAPL"}`; got != want {
		t.Errorf("the server received %s, want %s", got, want)
	}

	// The send frame comes first, exactly as authored — no "at": it is not
	// something the server did — then everything that came back, in order.
	want := []struct {
		zeroAt        bool
		at            string
		send, receive string
	}{
		{zeroAt: true, send: `{"type":"subscribe","symbol":"AAPL"}`},
		{at: "100ms", receive: `{"type":"trade","data":[{"s":"AAPL","p":190.5}]}`},
		{at: "200ms", receive: `{"type":"ping"}`},
		{at: "300ms", receive: `{"type":"trade","data":[{"s":"AAPL","p":190.6}]}`},
	}

	if len(s.Frames) != len(want) {
		t.Fatalf("got %d frames, want %d", len(s.Frames), len(want))
	}

	for i, w := range want {
		f := s.Frames[i]

		if w.zeroAt {
			if !f.At.IsZero() {
				t.Errorf("frames[%d].at: got %s, want unset", i, f.At)
			}
		} else if f.At.String() != w.at {
			t.Errorf("frames[%d].at: got %s, want %s", i, f.At, w.at)
		}

		if string(f.Send) != w.send {
			t.Errorf("frames[%d].send: got %s, want %s", i, f.Send, w.send)
		}

		if string(f.Receive) != w.receive {
			t.Errorf("frames[%d].receive: got %s, want %s", i, f.Receive, w.receive)
		}
	}

	if !sr.Complete() {
		t.Errorf("the session did not meet its condition: %s", sr)
	}

	if want := "3 received (ping=1, trade=2)"; sr.String() != want {
		t.Errorf("got %q, want %q", sr, want)
	}
}

// TestRecordSessionTimeout is the case that matters most: a feed that never
// sends what you were waiting for is an answer, not an error.
func TestRecordSessionTimeout(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) {
		writeAll(t, conn, `{"type":"trade"}`)

		// Hold the connection open and send nothing else.
		<-time.After(time.Second)
	})

	s := &enrich.Session{URI: uri}

	r := &enrich.Recorder{Until: &enrich.Until{
		Timeout:       200 * time.Millisecond,
		Discriminator: "type",
		Kinds:         map[string]int{"trade": 1, "ping": 1},
	}}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if sr.Complete() {
		t.Fatal("the session reported complete, but no ping was ever sent")
	}

	if want := "1 received (trade=1); never saw 1 more ping"; sr.String() != want {
		t.Errorf("got %q, want %q", sr, want)
	}
}

// TestRecordSessionNotJSON is the Yahoo case: a feed whose frames are not JSON
// is still recorded, kept as strings, and counted — never dropped and never an
// error, because what it sent is the evidence.
func TestRecordSessionNotJSON(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) {
		writeAll(t, conn, `CgRBQVBMFQAAP0M=`)

		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x0a, 0x04, 0x41}); err != nil {
			t.Errorf("writing: %v", err)
		}
	})

	s := &enrich.Session{URI: uri}

	r := &enrich.Recorder{Until: &enrich.Until{
		Timeout:  10 * time.Second,
		Messages: 2,
	}}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	want := []string{
		`"CgRBQVBMFQAAP0M="`, // text that is not JSON, kept as a string
		`"CgRB"`,             // a binary frame, kept as base64
	}

	for i, w := range want {
		if got := string(s.Frames[i].Receive); got != w {
			t.Errorf("frames[%d].receive: got %s, want %s", i, got, w)
		}
	}

	if want := "2 received; 2 not JSON"; sr.String() != want {
		t.Errorf("got %q, want %q", sr, want)
	}
}

// TestRecordSessionKeepsEnvRef checks that the URI is written back as authored:
// a reference to an environment variable is what keeps the file runnable, so
// masking must not eat it.
func TestRecordSessionKeepsEnvRef(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) { writeAll(t, conn, `{"ok":true}`) })

	t.Setenv("TEST_RECORD_TOKEN", "s3cret")

	authored := uri + "?token=$TEST_RECORD_TOKEN"
	s := &enrich.Session{URI: authored}

	r := &enrich.Recorder{Until: &enrich.Until{Timeout: 10 * time.Second, Messages: 1}}

	if _, err := r.Session(context.Background(), s); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if s.URI != authored {
		t.Errorf("got %s, want %s", s.URI, authored)
	}

	if strings.Contains(s.URI, "s3cret") {
		t.Error("the expanded credential was written back")
	}
}

// TestRecordSessionMasksLiteralURI checks the other half: a URI that carries a
// credential outright, rather than a reference to one, does not survive.
func TestRecordSessionMasksLiteralURI(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) { writeAll(t, conn, `{"ok":true}`) })

	s := &enrich.Session{URI: uri + "?token=s3cret&symbol=AAPL"}

	r := &enrich.Recorder{Until: &enrich.Until{Timeout: 10 * time.Second, Messages: 1}}

	if _, err := r.Session(context.Background(), s); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if strings.Contains(s.URI, "s3cret") {
		t.Errorf("the credential survived: %s", s.URI)
	}

	if !strings.Contains(s.URI, "symbol=AAPL") {
		t.Errorf("an ordinary parameter was lost: %s", s.URI)
	}
}

// TestRecordSessionMasksFrames checks that a credential a server puts in a
// payload does not survive into the recording.
func TestRecordSessionMasksFrames(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) {
		writeAll(t, conn, `{"type":"auth","session":"3f2c-live-session-token","ok":true}`)
	})

	s := &enrich.Session{URI: uri}

	r := &enrich.Recorder{Until: &enrich.Until{Timeout: 10 * time.Second, Messages: 1}}

	if _, err := r.Session(context.Background(), s); err != nil {
		t.Fatalf("recording: %v", err)
	}

	want := `{"type":"auth","session":"***","ok":true}`
	if got := string(s.Frames[0].Receive); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRecordNoURI(t *testing.T) {
	r := &enrich.Recorder{Until: &enrich.Until{Timeout: time.Second}}

	_, err := r.Session(context.Background(), &enrich.Session{})
	if err == nil {
		t.Fatal("got no error, want one")
	}

	if want := "uri is required"; err.Error() != want {
		t.Errorf("got %q, want %q", err, want)
	}
}

// TestRecordSkipsAlreadySatisfied checks the resumability guarantee: a session
// whose existing frames already meet the stop condition is not dialled again.
// The dialer here fails any call, so a dial happening at all fails the test.
func TestRecordSkipsAlreadySatisfied(t *testing.T) {
	s := &enrich.Session{
		URI: "ws://should-not-be-dialled.invalid",
		Frames: []*enrich.Frame{
			{Send: jsontext.Value(`{"type":"subscribe","symbol":"AAPL"}`)},
			{At: enrich.NewDuration(100 * time.Millisecond), Receive: jsontext.Value(`{"type":"trade"}`)},
			{At: enrich.NewDuration(200 * time.Millisecond), Receive: jsontext.Value(`{"type":"trade"}`)},
		},
	}

	r := &enrich.Recorder{
		Until: &enrich.Until{
			Timeout:       10 * time.Second,
			Discriminator: "type",
			Kinds:         map[string]int{"trade": 2},
		},
		Dialer: &websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			t.Fatal("dialled a session that was already complete")

			return nil, nil
		}},
	}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if !sr.Skipped {
		t.Error("got Skipped = false, want true")
	}

	if want := "already complete, skipped — 2 received (trade=2)"; sr.String() != want {
		t.Errorf("got %q, want %q", sr, want)
	}
}

// TestRecordRestartsWhenIncomplete checks the other half of resumability: a
// session that falls short of a new, stricter condition is recorded again from
// scratch, rather than trying to extend what it already had.
func TestRecordRestartsWhenIncomplete(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) {
		writeAll(t, conn, `{"type":"trade"}`, `{"type":"trade"}`, `{"type":"trade"}`)
	})

	s := &enrich.Session{
		URI: uri,
		Frames: []*enrich.Frame{
			{Send: jsontext.Value(`{"type":"subscribe","symbol":"AAPL"}`)},
			// Only one trade was captured last time — asking for three now
			// cannot be satisfied by extending this, since it is not the
			// connection that is about to be opened.
			{At: enrich.NewDuration(50 * time.Millisecond), Receive: jsontext.Value(`{"type":"trade"}`)},
		},
	}

	r := &enrich.Recorder{Until: &enrich.Until{
		Timeout:       10 * time.Second,
		Discriminator: "type",
		Kinds:         map[string]int{"trade": 3},
	}}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if sr.Skipped {
		t.Error("got Skipped = true, want false")
	}

	if got := sr.Seen["trade"]; got != 3 {
		t.Errorf("got %d trades, want 3", got)
	}

	// Exactly one send frame, exactly the authored one — the stale receive
	// from before is gone, not appended to.
	sends := 0

	for _, f := range s.Frames {
		if len(f.Send) > 0 {
			sends++
		}
	}

	if sends != 1 {
		t.Errorf("got %d send frames, want 1", sends)
	}
}

// TestRecordParallel checks that sessions record concurrently rather than one
// after another: two servers each hold their connection open until both have
// connected, which only both closing in time proves happened at once.
func TestRecordParallel(t *testing.T) {
	var wg sync.WaitGroup

	wg.Add(2)

	block := func(conn *websocket.Conn) {
		wg.Done()
		wg.Wait() // blocks forever if the other session has not also connected

		writeAll(t, conn, `{"ok":true}`)
	}

	uriA, uriB := serve(t, block), serve(t, block)

	ss := enrich.Sessions{{URI: uriA}, {URI: uriB}}

	r := &enrich.Recorder{Until: &enrich.Until{Timeout: 2 * time.Second, Messages: 1}}

	done := make(chan error, 1)

	go func() {
		_, err := r.Record(context.Background(), ss)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("recording: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — the sessions were not recorded concurrently")
	}
}

// TestRecordSave checks that every captured frame is persisted as it arrives,
// not only once recording finishes — so a crash mid-run loses at most one frame.
func TestRecordSave(t *testing.T) {
	uri := serve(t, func(conn *websocket.Conn) {
		writeAll(t, conn, `{"type":"trade"}`, `{"type":"trade"}`)
	})

	s := &enrich.Session{URI: uri}
	ss := enrich.Sessions{s}

	var saves []int // the frame count at each save, to see it grow one at a time

	var mu sync.Mutex

	r := &enrich.Recorder{
		Until: &enrich.Until{Timeout: 10 * time.Second, Messages: 2},
		Save: func(got enrich.Sessions) error {
			mu.Lock()
			defer mu.Unlock()

			saves = append(saves, len(got[0].Frames))

			return nil
		},
	}

	if _, err := r.Record(context.Background(), ss); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if len(saves) < 2 {
		t.Fatalf("got %d saves, want at least 2 — one per received frame", len(saves))
	}

	for i := 1; i < len(saves); i++ {
		if saves[i] < saves[i-1] {
			t.Errorf("save %d saw %d frames after save %d saw %d — went backwards", i, saves[i], i-1, saves[i-1])
		}
	}
}

// TestRecordUnsubscribeAndClose checks that an authored Unsubscribe frame is
// sent, and that the connection ends with a real WebSocket close handshake
// (RFC 6455 §7.1.1) rather than being cut.
func TestRecordUnsubscribeAndClose(t *testing.T) {
	var (
		gotUnsubscribe string
		gotCloseCode   int
	)

	serverDone := make(chan struct{})

	uri := serve(t, func(conn *websocket.Conn) {
		defer close(serverDone)

		writeAll(t, conn, `{"type":"trade"}`)

		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("reading the unsubscribe: %v", err)

			return
		}

		gotUnsubscribe = string(data)

		_, _, err = conn.ReadMessage()

		var ce *websocket.CloseError
		if ok := errors.As(err, &ce); !ok {
			t.Errorf("got %v, want a close error", err)

			return
		}

		gotCloseCode = ce.Code
	})

	s := &enrich.Session{
		URI:         uri,
		Unsubscribe: jsontext.Value(`{"type":"unsubscribe","symbol":"AAPL"}`),
	}

	r := &enrich.Recorder{Until: &enrich.Until{Timeout: 10 * time.Second, Messages: 1}}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the server never observed the close handshake")
	}

	if !sr.Complete() {
		t.Fatalf("the session did not meet its condition: %s", sr)
	}

	if want := `{"type":"unsubscribe","symbol":"AAPL"}`; gotUnsubscribe != want {
		t.Errorf("got %s, want %s", gotUnsubscribe, want)
	}

	if gotCloseCode != websocket.CloseNormalClosure {
		t.Errorf("got close code %d, want %d", gotCloseCode, websocket.CloseNormalClosure)
	}

	// The unsubscribe is itself a send frame, recorded like any other.
	last := s.Frames[len(s.Frames)-1]
	if string(last.Send) != string(s.Unsubscribe) {
		t.Errorf("the unsubscribe frame was not recorded: %+v", last)
	}
}
