package enrich_test

import (
	"context"
	"encoding/json/jsontext"
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
		defer conn.Close()

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

	// The send frame comes first, then everything that came back, in order.
	want := []struct{ at, send, receive string }{
		{at: "100ms", send: `{"type":"subscribe","symbol":"AAPL"}`},
		{at: "200ms", receive: `{"type":"trade","data":[{"s":"AAPL","p":190.5}]}`},
		{at: "300ms", receive: `{"type":"ping"}`},
		{at: "400ms", receive: `{"type":"trade","data":[{"s":"AAPL","p":190.6}]}`},
	}

	if len(s.Frames) != len(want) {
		t.Fatalf("got %d frames, want %d", len(s.Frames), len(want))
	}

	for i, w := range want {
		f := s.Frames[i]

		if f.At.String() != w.at {
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
