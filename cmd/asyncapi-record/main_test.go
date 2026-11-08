package main

import (
	"bytes"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
	"github.com/gorilla/websocket"
)

// feed is a WebSocket server that answers every frame it reads with replies.
type feed struct {
	url   string
	dials atomic.Int32

	mu   sync.Mutex
	read []string
}

func newFeed(t *testing.T, replies ...string) *feed {
	t.Helper()

	f := &feed{}
	upgrader := websocket.Upgrader{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.dials.Add(1)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			f.mu.Lock()
			f.read = append(f.read, string(data))
			f.mu.Unlock()

			for _, msg := range replies {
				if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)

	f.url = "ws" + strings.TrimPrefix(srv.URL, "http")

	return f
}

func (f *feed) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.read...)
}

// sessionsFile writes a sessions file with one session against uri and
// returns its path.
func sessionsFile(t *testing.T, uri, extra string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sessions.json")
	data := `[{"uri":"` + uri + `","frames":[{"send":{"type":"subscribe","symbol":"AAPL"}}]` + extra + `}]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func compact(t *testing.T, v jsontext.Value) string {
	t.Helper()

	v = v.Clone()
	if err := v.Compact(); err != nil {
		t.Fatal(err)
	}

	return string(v)
}

func record(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	err = run(t.Context(), args, &out, &errOut)

	return out.String(), errOut.String(), err
}

// TestRunRecords checks the whole round trip: the authored frame is sent, the
// replies are written back to the file, the report goes to stdout, and a
// rerun whose frames already satisfy the condition dials nothing.
func TestRunRecords(t *testing.T) {
	f := newFeed(t, `{"type":"trade","p":1.5}`, `{"type":"ping"}`)
	path := sessionsFile(t, f.url, "")

	stdout, stderr, err := record(t, "-f", path, "-kinds", "trade=1,ping=1", "-timeout", "5s")
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if want := "sessions[0]: 2 received (ping=1, trade=1)\n"; stdout != want {
		t.Errorf("stdout: got %q, want %q", stdout, want)
	}

	if stderr != "" {
		t.Errorf("stderr: got %q, want nothing", stderr)
	}

	if got := f.received(); len(got) == 0 || got[0] != `{"type":"subscribe","symbol":"AAPL"}` {
		t.Errorf("the server read %q, want the subscribe frame first", got)
	}

	ss, err := enrich.LoadFromFile(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	if n := len(ss[0].Frames); n != 3 {
		t.Fatalf("frames: got %d, want 3", n)
	}

	if got := compact(t, ss[0].Frames[2].Receive); got != `{"type":"ping"}` {
		t.Errorf("last frame: got %s, want the ping", got)
	}

	stdout, _, err = record(t, "-f", path, "-kinds", "trade=1,ping=1", "-timeout", "5s")
	if err != nil {
		t.Fatalf("rerunning: %v", err)
	}

	if !strings.Contains(stdout, "already complete, skipped") {
		t.Errorf("rerun stdout: got %q, want it skipped", stdout)
	}

	if n := f.dials.Load(); n != 1 {
		t.Errorf("dials: got %d, want 1", n)
	}
}

// TestRunUnmetConditions checks that a timeout with a kind never seen is
// reported on stdout and explained on stderr, and is not an error.
func TestRunUnmetConditions(t *testing.T) {
	f := newFeed(t, `{"type":"trade"}`)
	path := sessionsFile(t, f.url, "")

	stdout, stderr, err := record(t, "-f", path, "-kinds", "pong=1", "-timeout", "200ms")
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if !strings.Contains(stdout, "never saw 1 more pong") {
		t.Errorf("stdout: got %q, want the missing pong", stdout)
	}

	if !strings.Contains(stderr, "Some conditions went unmet") {
		t.Errorf("stderr: got %q, want the unmet conditions explained", stderr)
	}
}

// TestRunMessagesAndDiscriminator checks the -messages and -discriminator
// flags reach the recorder.
func TestRunMessagesAndDiscriminator(t *testing.T) {
	f := newFeed(t, `{"event":"a"}`, `{"event":"b"}`, `{"event":"a"}`, `{"event":"b"}`)
	path := sessionsFile(t, f.url, `,"unsubscribe":{"type":"unsubscribe"}`)

	stdout, _, err := record(t, "-f", path, "-messages", "3", "-discriminator", "event", "-timeout", "5s")
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	if want := "sessions[0]: 3 received (a=2, b=1)\n"; stdout != want {
		t.Errorf("stdout: got %q, want %q", stdout, want)
	}

	ss, err := enrich.LoadFromFile(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	last := ss[0].Frames[len(ss[0].Frames)-1]
	if got := compact(t, last.Send); got != `{"type":"unsubscribe"}` {
		t.Errorf("last frame: got %s, want the unsubscribe", got)
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	noURI := filepath.Join(dir, "nouri.json")

	if err := os.WriteFile(noURI, []byte(`[{"frames":[]}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	good := sessionsFile(t, "ws://127.0.0.1:1", "")

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown flag":        {[]string{"-nope"}, "flag provided but not defined"},
		"kinds without count": {[]string{"-kinds", "trade"}, "kind=count"},
		"kinds bad count":     {[]string{"-kinds", "trade=x"}, `count of "trade"`},
		"missing file":        {[]string{"-f", filepath.Join(dir, "missing.json")}, "no such file"},
		"session without uri": {[]string{"-f", noURI}, "uri is required"},
		"no timeout":          {[]string{"-f", good, "-messages", "1", "-timeout", "0s"}, "timeout"},
		"no discriminator":    {[]string{"-f", good, "-kinds", "a=1", "-discriminator", ""}, "discriminator"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := record(t, tc.args...)
			if err == nil {
				t.Fatal("want an error, got none")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error: got %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestKindsSet(t *testing.T) {
	k := kinds{}
	if err := k.Set("trade=3,ping=1"); err != nil {
		t.Fatal(err)
	}

	if k["trade"] != 3 || k["ping"] != 1 || len(k) != 2 {
		t.Errorf("got %v, want trade=3 ping=1", k)
	}

	if got := k.String(); got != "map[ping:1 trade:3]" {
		t.Errorf("String: got %q", got)
	}
}

// TestRunSavesAsItGoes checks that a frame is on disk before recording
// finishes, so a crash mid-run keeps what had arrived.
func TestRunSavesAsItGoes(t *testing.T) {
	var (
		path    string
		onDisk  atomic.Bool
		upgrade = websocket.Upgrader{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck

		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}

		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"first"}`)); err != nil {
			return
		}

		// Hold the second frame back until the first is in the file.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(`"first"`)) {
				onDisk.Store(true)

				break
			}

			time.Sleep(5 * time.Millisecond)
		}

		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"second"}`))

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	path = sessionsFile(t, "ws"+strings.TrimPrefix(srv.URL, "http"), "")

	if _, _, err := record(t, "-f", path, "-kinds", "second=1", "-timeout", "5s"); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if !onDisk.Load() {
		t.Error("the first frame was not saved before recording finished")
	}
}
