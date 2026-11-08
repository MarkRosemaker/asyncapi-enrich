package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
)

const sessions = `[
  {
    "uri": "wss://ws.example.com/feed?token=$TOKEN",
    "frames": [
      {"send": {"type": "subscribe", "symbol": "AAPL"}},
      {"at": "10ms", "receive": {"type": "trade", "p": 1.5, "s": "AAPL"}},
      {"at": "20ms", "receive": {"type": "ping"}}
    ]
  }
]`

// writeFile writes data to name in dir and returns its path.
func writeFile(t *testing.T, dir, name, data string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// TestRunCreatesSpec checks that a missing spec starts from a new document and
// is written with everything the sessions taught, and that a second run on the
// now existing spec leaves the file exactly as it was.
func TestRunCreatesSpec(t *testing.T) {
	for _, ext := range []string{".json", ".yaml"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			sessPath := writeFile(t, dir, "sessions.json", sessions)
			specPath := filepath.Join(dir, "asyncapi"+ext)
			args := []string{"-spec", specPath, "-sessions", sessPath}

			if err := run(args, &bytes.Buffer{}); err != nil {
				t.Fatalf("first run: %v", err)
			}

			first := readFile(t, specPath)

			doc, err := asyncapi.LoadFromFile(specPath)
			if err != nil {
				t.Fatalf("loading the written spec: %v", err)
			}

			if err := doc.Validate(); err != nil {
				t.Fatalf("the written spec does not validate: %v", err)
			}

			if _, ok := doc.Servers["ws-example-com"]; !ok {
				t.Errorf("no server for ws.example.com")
			}

			for _, name := range []string{"Subscribe", "Trade", "Ping"} {
				if _, ok := doc.Components.Schemas[name]; !ok {
					t.Errorf("no schema %q", name)
				}
			}

			if _, ok := doc.Components.SecuritySchemes["token"]; !ok {
				t.Errorf("no security scheme for the token query parameter")
			}

			if err := run(args, &bytes.Buffer{}); err != nil {
				t.Fatalf("second run: %v", err)
			}

			if second := readFile(t, specPath); !bytes.Equal(first, second) {
				t.Errorf("a rerun changed the spec:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
		})
	}
}

// TestRunExtendsSpec checks that a new recording merges into the existing
// spec: a field one sample lacks stops being required.
func TestRunExtendsSpec(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "asyncapi.json")

	first := writeFile(t, dir, "first.json", `[{"uri":"ws://example.invalid","frames":[{"at":"1ms","receive":{"type":"trade","p":1,"s":"A"}}]}]`)
	if err := run([]string{"-spec", specPath, "-sessions", first}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	second := writeFile(t, dir, "second.json", `[{"uri":"ws://example.invalid","frames":[{"at":"1ms","receive":{"type":"trade","p":2}}]}]`)
	if err := run([]string{"-spec", specPath, "-sessions", second}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	doc, err := asyncapi.LoadFromFile(specPath)
	if err != nil {
		t.Fatal(err)
	}

	trade := doc.Components.Schemas["Trade"].Value.Schema
	if got := strings.Join(trade.Required, ","); got != "type,p" {
		t.Errorf("required: got %q, want %q", got, "type,p")
	}

	if _, ok := trade.Properties["s"]; !ok {
		t.Errorf("the optional field s was dropped")
	}
}

// TestRunKeepsInvalidSpec checks that a spec already invalid before the run is
// still enriched and written, rather than refused for a problem this tool did
// not cause.
func TestRunKeepsInvalidSpec(t *testing.T) {
	dir := t.TempDir()
	specPath := writeFile(t, dir, "asyncapi.json", `{"asyncapi":"3.1.0","info":{"title":"","version":"1"}}`)
	sessPath := writeFile(t, dir, "sessions.json", sessions)

	if err := run([]string{"-spec", specPath, "-sessions", sessPath}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !bytes.Contains(readFile(t, specPath), []byte(`"Trade"`)) {
		t.Errorf("the spec was not enriched")
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, dir, "sessions.json", sessions)
	noURI := writeFile(t, dir, "nouri.json", `[{"frames":[]}]`)
	badJSON := writeFile(t, dir, "bad.json", `[`)
	badSpec := writeFile(t, dir, "badspec.json", `{`)
	spec := filepath.Join(dir, "asyncapi.json")

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown flag":        {[]string{"-nope"}, "flag provided but not defined"},
		"missing sessions":    {[]string{"-spec", spec, "-sessions", filepath.Join(dir, "missing.json")}, "no such file"},
		"malformed sessions":  {[]string{"-spec", spec, "-sessions", badJSON}, "parsing"},
		"session without uri": {[]string{"-spec", spec, "-sessions", noURI}, "uri is required"},
		"malformed spec":      {[]string{"-spec", badSpec, "-sessions", good}, ""},
		"unsupported spec":    {[]string{"-spec", filepath.Join(dir, "asyncapi.txt"), "-sessions", good}, "unsupported file extension"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(tc.args, &bytes.Buffer{})
			if err == nil {
				t.Fatal("want an error, got none")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error: got %q, want it to contain %q", err, tc.want)
			}
		})
	}

	if _, err := os.Stat(spec); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed run wrote the spec anyway: %v", err)
	}
}
