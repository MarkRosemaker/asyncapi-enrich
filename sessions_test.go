package enrich_test

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

func TestSessionsValidate(t *testing.T) {
	msg := jsontext.Value(`{"type":"x"}`)

	for name, tc := range map[string]struct {
		ss       enrich.Sessions
		want     error
		wantText string
	}{
		"valid": {ss: enrich.Sessions{{URI: "ws://a", Frames: []*enrich.Frame{{Send: msg}, {Receive: msg}}}}},
		"no uri": {
			ss:   enrich.Sessions{{URI: "ws://a"}, {}},
			want: enrich.ErrNoURI, wantText: "[1]: ",
		},
		"both directions": {
			ss:   enrich.Sessions{{URI: "ws://a", Frames: []*enrich.Frame{{Send: msg}, {Send: msg, Receive: msg}}}},
			want: enrich.ErrBothDirections, wantText: "[0]: frames[1]: ",
		},
		"no direction": {
			ss:   enrich.Sessions{{URI: "ws://a", Frames: []*enrich.Frame{{}}}},
			want: enrich.ErrNoDirection, wantText: "[0]: frames[0]: ",
		},
		"unparsable uri": {
			ss:       enrich.Sessions{{URI: "ws://a b:x"}},
			wantText: "[0]: uri: ",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.ss.Validate()

			if tc.want == nil && tc.wantText == "" {
				if err != nil {
					t.Fatalf("got %v, want no error", err)
				}

				return
			}

			if err == nil {
				t.Fatal("want an error, got none")
			}

			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}

			if !strings.HasPrefix(err.Error(), tc.wantText) {
				t.Errorf("got %q, want it to start with %q", err, tc.wantText)
			}
		})
	}
}

func TestUntilValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		until enrich.Until
		want  error
	}{
		"timeout only":          {until: enrich.Until{Timeout: time.Second}},
		"kinds with field":      {until: enrich.Until{Timeout: time.Second, Discriminator: "type", Kinds: map[string]int{"a": 1}}},
		"no timeout":            {until: enrich.Until{Messages: 1}, want: enrich.ErrNoTimeout},
		"negative timeout":      {until: enrich.Until{Timeout: -time.Second}, want: enrich.ErrNoTimeout},
		"kinds without a field": {until: enrich.Until{Timeout: time.Second, Kinds: map[string]int{"a": 1}}, want: enrich.ErrNoDiscriminator},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.until.Validate(); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReport(t *testing.T) {
	r := &enrich.Report{Sessions: []*enrich.SessionReport{
		{Skipped: true, Received: 3, Seen: map[string]int{"trade": 2, "ping": 1}},
		{Received: 1, Short: 4, Missing: map[string]int{"ping": 1, "auth": 2}, NotJSON: 1},
	}}

	want := "sessions[0]: already complete, skipped — 3 received (ping=1, trade=2)\n" +
		"sessions[1]: 1 received; 1 not JSON; 4 short; never saw 2 more auth; never saw 1 more ping"
	if got := r.String(); got != want {
		t.Errorf("String:\ngot  %q\nwant %q", got, want)
	}

	if r.Complete() {
		t.Error("Complete: got true with the second session short")
	}

	r.Sessions = r.Sessions[:1]
	if !r.Complete() {
		t.Error("Complete: got false with every session complete")
	}
}

// TestDurationKeepsText checks that an arrival time reads as a duration and is
// written back in the text it was authored in.
func TestDurationKeepsText(t *testing.T) {
	ss, err := enrich.ParseSessions([]byte(`[{"uri":"ws://a","frames":[{"at":"60s","receive":1}]}]`))
	if err != nil {
		t.Fatal(err)
	}

	at := ss[0].Frames[0].At
	if at.Duration() != time.Minute {
		t.Errorf("Duration: got %v, want 1m", at.Duration())
	}

	if at.String() != "60s" {
		t.Errorf("String: got %q, want 60s", at)
	}

	if got := enrich.NewDuration(1500 * time.Millisecond).String(); got != "1.5s" {
		t.Errorf("NewDuration String: got %q, want 1.5s", got)
	}
}

func TestLoadFromFileErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := enrich.LoadFromFile(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: got %v, want not-exist", err)
	}

	for name, data := range map[string]string{
		"not json":     `[`,
		"bad duration": `[{"uri":"ws://a","frames":[{"at":"soon","receive":1}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := enrich.LoadFromFile(path)
			if err == nil || !strings.Contains(err.Error(), "parsing "+path) {
				t.Errorf("got %v, want a parse error naming the file", err)
			}
		})
	}
}
