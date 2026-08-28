package enrich_test

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

//go:embed testdata
var testdata embed.FS

// TestEnrich_TestData enriches a document from every testdata/<fixture>/api,
// starting from its asyncapi.json if it has one and from [enrich.NewDocument]
// otherwise, and compares the result against golden.json.
//
// Enrich runs three times per fixture, not once: [enrich.Enrich] is meant to be
// called again every time a new recording comes in, and running it against a
// document it already enriched must reach the same result rather than drift —
// duplicating a schema, renaming an operation, or growing an examples list
// without bound. If iteration one and iteration three differ, that drift is
// the bug the third run exists to catch.
func TestEnrich_TestData(t *testing.T) {
	entries, err := testdata.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range entries {
		t.Run(tc.Name(), func(t *testing.T) {
			data, err := testdata.ReadFile(filepath.Join("testdata", tc.Name(), "api", "sessions.json"))
			if err != nil {
				t.Fatal(err)
			}

			ss, err := enrich.ParseSessions(data)
			if err != nil {
				t.Fatal(err)
			}

			wantDoc, err := testdata.ReadFile(filepath.Join("testdata", tc.Name(), "api", "golden.json"))
			if err != nil {
				t.Fatal(err)
			}

			doc := enrich.NewDocument()

			if data, err := testdata.ReadFile(filepath.Join("testdata", tc.Name(), "api", "asyncapi.json")); err == nil {
				doc, err = asyncapi.LoadFromDataJSON(data)
				if err != nil {
					t.Fatalf("loading initial spec: %v", err)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}

			for it := range 3 {
				t.Run(fmt.Sprintf("iteration %d", it+1), func(t *testing.T) {
					if err := enrich.Enrich(doc, ss); err != nil {
						t.Fatal(err)
					}

					if err := doc.Validate(); err != nil {
						t.Fatal(err)
					}

					gotDoc, err := doc.ToJSON()
					if err != nil {
						t.Fatal(err)
					}

					compareBytes(t, wantDoc, gotDoc)
				})
			}
		})
	}
}

// compareBytes prints a compact diff of two byte slices.
func compareBytes(t *testing.T, want, got []byte) {
	t.Helper()

	if bytes.Equal(want, got) {
		return
	}

	t.Errorf("documents differ:\nwant:\n%s\ngot:\n%s", want, got)
}
