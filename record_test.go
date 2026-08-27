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
		t  = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
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

// readOne reads a single message and reports what it was, so that a test can
// assert the server actually received what the session said to send.
func readOne(t *testing.T, conn *websocket.Conn) string {
	t.Helper()

	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Errorf("reading: %v", err)

		return ""
	}

	return string(data)
}

func TestRecordSession(t *testing.T) {
	var got string

	url := serve(t, func(conn *websocket.Conn) {
		got = readOne(t, conn)

		for _, msg := range []string{
			`{"type":"trade","data":[{"s":"AAPL","p":190.5}]}`,
			`{"type":"ping"}`,
			`{"type":"trade","data":[{"s":"AAPL","p":190.6}]}`,
		} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				t.Errorf("writing: %v", err)

				return
			}
		}
	})

	s := &enrich.Session{
		Server: "production",
		Until: &enrich.Until{
			Timeout:       enrich.NewDuration(10 * time.Second),
			Discriminator: "type",
			Kinds:         map[string]int{"trade": 2, "ping": 1},
		},
		Frames: []*enrich.Frame{
			{Send: jsontext.Value(`{"type":"subscribe","symbol":"AAPL"}`)},
		},
	}

	r := &enrich.Recorder{
		URLs: map[string]string{"production": url},
		Now:  stepClock(100 * time.Millisecond),
	}

	sr, err := r.Session(context.Background(), s)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if want := `{"type":"subscribe","symbol":"AAPL"}`; got != want {
		t.Errorf("the server received %s, want %s", got, want)
	}

	// The send frame comes first, then everything that came back, in order.
	want := []struct {
		at      string
		send    string
		receive string
	}{
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

	if s.RecordedAt.IsZero() {
		t.Error("recordedAt was not set")
	}
}

// TestRecordSessionTimeout is the case that matters most: a feed that never
// sends what you were waiting for is an answer, not an error.
func TestRecordSessionTimeout(t *testing.T) {
	url := serve(t, func(conn *websocket.Conn) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"trade"}`)); err != nil {
			t.Errorf("writing: %v", err)

			return
		}

		// Hold the connection open and send nothing else.
		<-time.After(time.Second)
	})

	s := &enrich.Session{
		Server: "production",
		Until: &enrich.Until{
			Timeout:       enrich.NewDuration(200 * time.Millisecond),
			Discriminator: "type",
			Kinds:         map[string]int{"trade": 1, "ping": 1},
		},
	}

	r := &enrich.Recorder{URLs: map[string]string{"production": url}}

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

// TestRecordSessionMasks checks that a credential a server puts in a payload
// does not survive into the recording.
func TestRecordSessionMasks(t *testing.T) {
	url := serve(t, func(conn *websocket.Conn) {
		msg := `{"type":"auth","session":"3f2c-live-session-token","ok":true}`
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Errorf("writing: %v", err)
		}
	})

	s := &enrich.Session{
		Server: "production",
		Until: &enrich.Until{
			Timeout:  enrich.NewDuration(10 * time.Second),
			Messages: 1,
		},
	}

	r := &enrich.Recorder{URLs: map[string]string{"production": url}}

	if _, err := r.Session(context.Background(), s); err != nil {
		t.Fatalf("recording: %v", err)
	}

	want := `{"type":"auth","session":"***","ok":true}`
	if got := string(s.Frames[0].Receive); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRecordUnknownServer(t *testing.T) {
	s := &enrich.Session{
		Server: "production",
		Until:  &enrich.Until{Timeout: enrich.NewDuration(time.Second)},
	}

	_, err := (&enrich.Recorder{}).Session(context.Background(), s)
	if err == nil {
		t.Fatal("got no error, want one")
	}

	if want := `no URL for server "production"`; err.Error() != want {
		t.Errorf("got %q, want %q", err, want)
	}
}
