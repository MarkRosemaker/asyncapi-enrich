package enrich

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
)

// indent is the indentation of a sessions file. It is written back the way
// it is authored, so that recording a session shows up as a diff of the frames
// that arrived and nothing else.
const indent = "  "

// LoadFromFile reads a sessions file.
func LoadFromFile(name string) (Sessions, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}

	ss, err := ParseSessions(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}

	return ss, nil
}

// ParseSessions parses a sessions file already read into memory — from an
// embedded filesystem, say, where [LoadFromFile] cannot reach.
func ParseSessions(data []byte) (Sessions, error) {
	var ss Sessions
	if err := json.Unmarshal(data, &ss); err != nil {
		return nil, err
	}

	return ss, nil
}

// WriteToFile writes the sessions back, formatted the same way every time.
func (ss Sessions) WriteToFile(name string) error {
	data, err := json.Marshal(ss, jsontext.WithIndent(indent))
	if err != nil {
		return err
	}

	return os.WriteFile(name, append(data, '\n'), 0o644)
}
