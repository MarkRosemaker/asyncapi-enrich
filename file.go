package enrich

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
)

// indent is the indentation of an interactions file. It is written back the way
// it is authored, so that recording a session shows up as a diff of the frames
// that arrived and nothing else.
const indent = "  "

// LoadFromFile reads an interactions file.
func LoadFromFile(name string) (*Interactions, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}

	ixs := &Interactions{}
	if err := json.Unmarshal(data, ixs); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}

	return ixs, nil
}

// WriteToFile writes the interactions back, formatted the same way every time.
func (ixs *Interactions) WriteToFile(name string) error {
	data, err := json.Marshal(ixs, jsontext.WithIndent(indent))
	if err != nil {
		return err
	}

	return os.WriteFile(name, append(data, '\n'), 0o644)
}
