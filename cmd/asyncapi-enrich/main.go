// Command asyncapi-enrich enriches an AsyncAPI specification from a recorded
// sessions file.
//
// It is the half of asyncapi-enrich that needs no network, split out from
// asyncapi-record so it can run anywhere the specification is being written —
// which is not necessarily where the API was reachable to record it.
//
//	asyncapi-enrich -spec api/asyncapi.json -sessions api/sessions.json
//
// Running it again after a new recording is exactly the point, not a special
// case: a server, channel, message, or schema already in the spec is extended
// rather than duplicated, and a field only every sample has ever carried stays
// required — a field that turns out to be missing from one only earns
// "optional" the moment a recording without it exists to prove it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "asyncapi-enrich:", err)
		os.Exit(1)
	}
}

func run() error {
	specPath := flag.String("spec", "api/asyncapi.json", "path to the AsyncAPI spec file")
	sessionsPath := flag.String("sessions", "api/sessions.json", "path to the sessions file")
	flag.Parse()

	doc, err := asyncapi.LoadFromFile(*specPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}

		doc = enrich.NewDocument()
	}

	wasValid := doc.Validate() == nil

	ss, err := enrich.LoadFromFile(*sessionsPath)
	if err != nil {
		return err
	}

	if err := enrich.Enrich(doc, ss); err != nil {
		return err
	}

	// Only re-validate when the spec was already valid before this run, so
	// that an issue this run introduced is caught without this tool being the
	// one to refuse a document some other, unrelated problem already made
	// invalid. A brand new document has no channels or operations yet until
	// Enrich adds them, so it starts out invalid on purpose — that first run
	// skips this check the same way every later one that stays valid does not.
	if wasValid {
		if err := doc.Validate(); err != nil {
			return fmt.Errorf("produced an invalid document: %w", err)
		}
	}

	return doc.WriteToFile(*specPath)
}
