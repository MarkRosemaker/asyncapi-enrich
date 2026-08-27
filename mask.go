package enrich

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
)

// Replacement is what a masked value is replaced with.
const Replacement = "***"

// defaultMaskedFields are the field names whose values are masked unless the
// caller says otherwise. Matching is case-insensitive and ignores separators, so
// "api_key", "apiKey" and "API-KEY" are all the same name.
//
// The list errs on the side of masking. A field masked that did not need to be
// costs a schema one example; a field left unmasked costs a rotated credential.
var defaultMaskedFields = []string{
	"accesstoken",
	"apikey",
	"auth",
	"authorization",
	"cookie",
	"credential",
	"idtoken",
	"key",
	"password",
	"refreshtoken",
	"secret",
	"session",
	"sessionid",
	"sessiontoken",
	"signature",
	"token",
}

// Masker replaces the values of fields whose names look like credentials.
//
// It runs before anything is written to disk, never after. A recording that
// reaches a file unmasked has already leaked: a specification repository is
// public, and a credential committed to one is a credential to rotate.
type Masker struct {
	// fields are the normalized names to mask.
	fields []string
}

// NewMasker returns a Masker that masks the default field names and any extra
// names given. Pass nothing for the defaults alone.
func NewMasker(extra ...string) *Masker {
	fields := slices.Clone(defaultMaskedFields)

	for _, name := range extra {
		if name := normalizeFieldName(name); name != "" && !slices.Contains(fields, name) {
			fields = append(fields, name)
		}
	}

	slices.Sort(fields)

	return &Masker{fields: fields}
}

// normalizeFieldName lowercases a name and drops the separators that distinguish
// api_key from apiKey from API-KEY, none of which is a real difference.
func normalizeFieldName(name string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(name) {
		switch r {
		case '_', '-', '.', ' ':
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// masks reports whether a field of this name has its value replaced.
func (m *Masker) masks(name string) bool {
	return slices.Contains(m.fields, normalizeFieldName(name))
}

// URL returns the URL with the value of every query parameter that looks like a
// credential replaced, and any user information removed.
//
// This is the case openapi-enrich's masker does not cover and this one must: a
// WebSocket feed authenticated as wss://host/?token=… puts the secret in the URL
// itself, where masking headers and bodies never reaches it.
func (m *Masker) URL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}

	out := *u

	if out.User != nil {
		out.User = url.User(Replacement)
	}

	if q := out.Query(); len(q) > 0 {
		for name, vs := range q {
			if !m.masks(name) {
				continue
			}

			for i := range vs {
				vs[i] = Replacement
			}
		}

		out.RawQuery = q.Encode()
	}

	return &out
}

// Value returns the payload with the value of every field that looks like a
// credential replaced, at any depth. The order of the remaining fields is kept,
// so a masked recording still reads like what came off the wire.
func (m *Masker) Value(v jsontext.Value) (jsontext.Value, error) {
	if len(v) == 0 {
		return v, nil
	}

	var buf bytes.Buffer
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	enc := jsontext.NewEncoder(&buf)

	stack := stack{}

	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}

		// A string token in an object where a member name is expected is a name,
		// not a value, and names are never masked — only what follows them is.
		name := stack.expectsName() && tok.Kind() == '"'

		if err := enc.WriteToken(tok); err != nil {
			return nil, err
		}

		switch tok.Kind() {
		case '{':
			stack = append(stack, level{object: true, expectName: true})

			continue
		case '[':
			stack = append(stack, level{})

			continue
		case '}', ']':
			stack = stack[:len(stack)-1]
		default:
			if name {
				stack.setExpectName(false)

				if !m.masks(tok.String()) {
					continue
				}

				// Replace the whole value that follows, however deep it goes.
				if err := dec.SkipValue(); err != nil {
					return nil, err
				}

				if err := enc.WriteToken(jsontext.String(Replacement)); err != nil {
					return nil, err
				}
			}
		}

		stack.setExpectName(true)
	}

	return jsontext.Value(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// level is one open bracket: whether it is an object, and if so whether the next
// token in it is a member name rather than a value.
type level struct{ object, expectName bool }

// stack is the chain of brackets that are currently open.
type stack []level

// expectsName reports whether the next token is a member name of an object.
func (s stack) expectsName() bool {
	top := len(s) - 1

	return top >= 0 && s[top].object && s[top].expectName
}

// setExpectName records whether the next token of the innermost object is a
// member name. It does nothing inside an array, which has no names.
func (s stack) setExpectName(v bool) {
	if top := len(s) - 1; top >= 0 && s[top].object {
		s[top].expectName = v
	}
}

// Session masks every frame of a session in place.
func (m *Masker) Session(s *Session) error {
	for _, f := range s.Frames {
		for _, p := range []*jsontext.Value{&f.Send, &f.Receive} {
			masked, err := m.Value(*p)
			if err != nil {
				return err
			}

			*p = masked
		}
	}

	return nil
}
