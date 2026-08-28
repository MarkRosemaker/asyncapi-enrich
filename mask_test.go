package enrich_test

import (
	"encoding/json/jsontext"
	"net/url"
	"testing"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

func TestMaskerValue(t *testing.T) {
	m := enrich.NewMasker()

	for name, tc := range map[string]struct{ in, want string }{
		"nothing to mask": {
			`{"type":"trade","data":[{"s":"AAPL","p":1.5}]}`,
			`{"type":"trade","data":[{"s":"AAPL","p":1.5}]}`,
		},
		"top level": {
			`{"type":"auth","token":"abc123"}`,
			`{"type":"auth","token":"***"}`,
		},
		"separators and case are not a difference": {
			`{"api_key":"a","apiKey":"b","API-KEY":"c"}`,
			`{"api_key":"***","apiKey":"***","API-KEY":"***"}`,
		},
		"nested": {
			`{"a":{"b":{"password":"hunter2","keep":1}}}`,
			`{"a":{"b":{"password":"***","keep":1}}}`,
		},
		"inside an array": {
			`{"xs":[{"secret":"s","ok":true},{"ok":false}]}`,
			`{"xs":[{"secret":"***","ok":true},{"ok":false}]}`,
		},
		"an object value is replaced whole": {
			`{"auth":{"user":"u","pass":"p"},"after":1}`,
			`{"auth":"***","after":1}`,
		},
		"an array value is replaced whole": {
			`{"credential":[1,2,3],"after":1}`,
			`{"credential":"***","after":1}`,
		},
		"a field named like a credential is masked wherever it appears": {
			`{"session":{"id":"x"},"data":{"session":"y"}}`,
			`{"session":"***","data":{"session":"***"}}`,
		},
		"a string that happens to equal a masked name is not a name": {
			`{"kind":"token","token":"real"}`,
			`{"kind":"token","token":"***"}`,
		},
		"top level array": {
			`[{"token":"a"},{"b":2}]`,
			`[{"token":"***"},{"b":2}]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := m.Value(jsontext.Value(tc.in))
			if err != nil {
				t.Fatalf("masking: %v", err)
			}

			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestMaskerValueEmpty(t *testing.T) {
	got, err := enrich.NewMasker().Value(nil)
	if err != nil {
		t.Fatalf("masking: %v", err)
	}

	if got != nil {
		t.Errorf("got %s, want nil", got)
	}
}

func TestMaskerExtraFields(t *testing.T) {
	m := enrich.NewMasker("account_id")

	got, err := m.Value(jsontext.Value(`{"accountId":"U123","symbol":"AAPL"}`))
	if err != nil {
		t.Fatalf("masking: %v", err)
	}

	if want := `{"accountId":"***","symbol":"AAPL"}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestMaskerURL(t *testing.T) {
	m := enrich.NewMasker()

	for name, tc := range map[string]struct{ in, want string }{
		"the finnhub case": {"wss://ws.finnhub.io?token=abc123", "wss://ws.finnhub.io?token=%2A%2A%2A"},
		"nothing to mask":  {"wss://streamer.finance.yahoo.com/?version=2", "wss://streamer.finance.yahoo.com/?version=2"},
		"no query at all":  {"wss://localhost:5000/v1/api/ws", "wss://localhost:5000/v1/api/ws"},
		// url.User escapes the replacement, since "*" is not allowed unescaped in
		// user information. It is still unmistakably not a credential.
		"user information":  {"wss://user:pass@host/ws", "wss://%2A%2A%2A@host/ws"},
		"several to mask":   {"wss://h/?token=a&key=b&x=1", "wss://h/?key=%2A%2A%2A&token=%2A%2A%2A&x=1"},
		"a value kept once": {"wss://h/?symbol=AAPL", "wss://h/?symbol=AAPL"},
		// An env reference is what keeps a sessions file runnable. Masking it, or
		// even re-encoding the query around it, would break that.
		"an env reference is not a credential": {
			"wss://ws.finnhub.io?token=$FINNHUB_API_KEY",
			"wss://ws.finnhub.io?token=$FINNHUB_API_KEY",
		},
	} {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(tc.in)
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}

			if got := m.URL(u).String(); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}

			if u.String() != tc.in {
				t.Errorf("the input was modified: %s", u)
			}
		})
	}
}
