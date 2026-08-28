package enrich_test

import (
	"os"
	"path/filepath"
	"testing"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// TestLoadFixtures checks that every hand-authored sessions file is one the
// recorder would accept, so that a typo is found before a session is dialled
// rather than after.
func TestLoadFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/*/api/sessions.json")
	if err != nil {
		t.Fatalf("globbing: %v", err)
	}

	if len(paths) == 0 {
		t.Fatal("no fixtures found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			ss, err := enrich.LoadFromFile(path)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			if err := ss.Validate(); err != nil {
				t.Fatalf("validating: %v", err)
			}

			// The file is written back the way it is authored, so that recording
			// shows up as a diff of the frames that arrived and nothing else.
			out := filepath.Join(t.TempDir(), "sessions.json")
			if err := ss.WriteToFile(out); err != nil {
				t.Fatalf("writing: %v", err)
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}

			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}

			if string(got) != string(want) {
				t.Errorf("the file did not survive a round trip:\ngot\n%s\nwant\n%s", got, want)
			}
		})
	}
}
