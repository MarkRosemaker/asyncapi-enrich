// Command asyncapi-record plays the sessions of a sessions file against real
// servers and writes back what came over the wire.
//
// It is the half of asyncapi-enrich that needs the network, split out so that it
// can be run wherever the API is actually reachable — which is not necessarily
// where the specification is being written.
//
//	asyncapi-record -f api/sessions.json -kinds trade=3,ping=1 -timeout 60s
//
// A session's URI may refer to environment variables, which are expanded here
// rather than by the shell, so that the credential stays out of shell history.
// The expansion is used to dial and goes no further: the URI is written back
// exactly as it was authored, and every frame is masked before it reaches disk
// — as it is captured, not once at the end, so a crash mid-run never leaves a
// secret sitting on disk either.
//
// Every session in the file records concurrently. A session whose existing
// frames already satisfy the stop condition is left alone: rerunning after a
// successful capture costs nothing and dials nothing. One that falls short is
// recorded again from scratch — what it had came from a different connection
// with its own clock, so it cannot simply be extended.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// kinds collects a -kinds trade=3,ping=1 flag.
type kinds map[string]int

func (k kinds) String() string { return fmt.Sprintf("%v", map[string]int(k)) }

func (k kinds) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		name, count, ok := strings.Cut(part, "=")
		if !ok {
			return errors.New("must be of the form kind=count[,kind=count]")
		}

		n, err := strconv.Atoi(count)
		if err != nil {
			return fmt.Errorf("count of %q: %w", name, err)
		}

		k[name] = n
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "asyncapi-record:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		file    = flag.String("f", "api/sessions.json", "the sessions file to record into")
		timeout = flag.Duration("timeout", time.Minute, "how long to listen before giving up")
		msgs    = flag.Int("messages", 0, "how many received frames to wait for")
		discrim = flag.String("discriminator", "type", "the field naming the kind of a received message")
		ks      = kinds{}
	)

	flag.Var(ks, "kinds", "how many of each kind to wait for, e.g. trade=3,ping=1")
	flag.Parse()

	ss, err := enrich.LoadFromFile(*file)
	if err != nil {
		return err
	}

	// A recording that is interrupted still keeps what it has: the frames that
	// did arrive are the point, and they are worth more than a clean exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rec := &enrich.Recorder{
		Until: &enrich.Until{
			Timeout:       *timeout,
			Messages:      *msgs,
			Discriminator: *discrim,
			Kinds:         ks,
		},
		// Every frame is written to disk as it is captured, so a crash loses at
		// most the one frame in flight rather than the whole run.
		Save: func(ss enrich.Sessions) error { return ss.WriteToFile(*file) },
	}

	rep, recErr := rec.Record(ctx, ss)

	// Sessions record concurrently and each already saved itself as it went, but
	// one more write here is cheap insurance — it also covers a run where every
	// session was already complete and Save was never called at all.
	if err := ss.WriteToFile(*file); err != nil {
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
