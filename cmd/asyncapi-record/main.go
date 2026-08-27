// Command asyncapi-record plays the sessions of an interactions file against
// real servers and writes back what came over the wire.
//
// It is the half of asyncapi-enrich that needs the network, split out so that it
// can be run wherever the API is actually reachable — which is not necessarily
// where the specification is being written.
//
//	asyncapi-record -f api/interactions.json \
//	    -url 'production=wss://ws.finnhub.io?token=$FINNHUB_API_KEY'
//
// A URL may refer to environment variables, which are expanded here rather than
// by the shell, so that the credential stays out of the shell's history. It is
// used to dial and goes no further: what is written back names the server by the
// key it was given, and every frame is masked before it reaches the file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// urls collects repeated -url key=value flags.
type urls map[string]string

func (u urls) String() string { return fmt.Sprintf("%v", map[string]string(u)) }

func (u urls) Set(v string) error {
	key, raw, ok := strings.Cut(v, "=")
	if !ok {
		return errors.New(`must be of the form server=url`)
	}

	if key == "" {
		return errors.New("must name a server")
	}

	u[key] = os.ExpandEnv(raw)

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "asyncapi-record:", err)
		os.Exit(1)
	}
}

func run() error {
	file := flag.String("f", "api/interactions.json", "the interactions file to record into")
	us := urls{}
	flag.Var(us, "url", "the URL to dial for a server, as server=url; may be repeated")
	flag.Parse()

	if len(us) == 0 {
		return errors.New("no -url was given, so there is nothing to dial")
	}

	ixs, err := enrich.LoadFromFile(*file)
	if err != nil {
		return err
	}

	// A recording that is interrupted still keeps what it has: the frames that
	// did arrive are the point, and they are worth more than a clean exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rec := &enrich.Recorder{URLs: us}

	rep, recErr := rec.Record(ctx, ixs)

	if err := ixs.WriteToFile(*file); err != nil {
		return err
	}

	if recErr != nil {
		return recErr
	}

	fmt.Println(rep)

	if !rep.Complete() {
		fmt.Fprintln(os.Stderr,
			"\nSome conditions went unmet. That is a finding about the API, not a\n"+
				"failure: record again, or write the specification to say so.")
	}

	return nil
}
