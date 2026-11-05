package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

const (
	logDefault = 20
	logMost    = 100 // what the server gives at a time
)

// timeNow is time.Now; tests replace it.
var timeNow = time.Now

// logTime is when a line happened, in this machine's zone: the time of day,
// with the day when it wasn't today.
func logTime(at string) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return at
	}
	t = t.In(timeNow().Location())
	if t.Format("2006-01-02") == timeNow().Format("2006-01-02") {
		return t.Format("15:04:05")
	}
	return t.Format("Jan _2 15:04:05")
}

func runLog(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var follow bool
	fs.BoolVar(&follow, "f", false, "keep printing what happens, until Ctrl-C")
	fs.BoolVar(&follow, "follow", false, "the same as -f")
	lines := fs.Int("n", logDefault, "how many lines to show, up to 100")
	asJSON := fs.Bool("json", false, "print the lines as JSON (with -f, NDJSON: one object per line)")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: pony log [flags] [pony]

Show what a pony has been doing, oldest first, in the words of the Feed: the
cards it took, what it said, what it sent, what went wrong. With no pony,
every pony your token reaches. -f keeps printing what happens until Ctrl-C.

Flags:
`)
		fs.PrintDefaults()
	}
	pos, code, ok := parse(fs, args, 1)
	if !ok {
		return code
	}
	if *lines < 1 || *lines > logMost {
		fmt.Fprintf(stderr, "pony: -n is how many lines to show, 1 to %d\n", logMost)
		return ExitUsage
	}
	slug := ""
	if len(pos) == 1 {
		slug = pos[0]
	}
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}
	ctx, stop := interrupted()
	defer stop()

	// One JSON value when it's all there is to print; a stream when it goes on.
	show := func(e api.Event) {
		if *asJSON {
			printJSON(stdout, stderr, e)
			return
		}
		fmt.Fprintf(stdout, "%s  %s %s\n", logTime(e.At), e.Who, e.Text)
	}
	// Following, read all it can: the lines near the cursor come again, and
	// any it hadn't read would look new, though only the last few are shown.
	want := *lines
	if follow {
		want = logMost
	}
	log, err := c.ReadLog(ctx, slug, "", want)
	if err != nil {
		return logFailed(ctx, err, slug, c, stderr)
	}
	if !follow && *asJSON {
		if log.Events == nil {
			log.Events = []api.Event{}
		}
		return printJSON(stdout, stderr, log.Events)
	}
	for _, e := range log.Events[max(0, len(log.Events)-*lines):] {
		show(e)
	}
	if !follow {
		if len(log.Events) == 0 {
			fmt.Fprintln(stderr, "pony: nothing to show yet")
		}
		return ExitOK
	}
	return followLog(ctx, c, slug, log, show, stderr)
}

// followLog prints each new line until the context ends. The lines near the
// cursor come again on every read, so what the last read gave is all it has
// to remember.
func followLog(ctx context.Context, c *api.Client, slug string, log *api.Log, show func(api.Event), stderr io.Writer) int {
	seen := map[string]bool{}
	for _, e := range log.Events {
		seen[e.ID] = true
	}
	after := log.Next
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return 130
		case <-time.After(pollSlow):
		}
		log, err := c.ReadLog(ctx, slug, after, logMost)
		if err != nil {
			if ctx.Err() != nil {
				return 130
			}
			var ae *api.Error
			if errors.As(err, &ae) && ae.Status != 502 && ae.Status != 503 && ae.Status != 504 {
				return logFailed(ctx, err, slug, c, stderr) // not signed in, not found: reading again won't help
			}
			if failures++; failures > 5 {
				return logFailed(ctx, err, slug, c, stderr)
			}
			continue
		}
		failures = 0
		now := make(map[string]bool, len(log.Events))
		for _, e := range log.Events {
			now[e.ID] = true
			if !seen[e.ID] {
				show(e)
			}
		}
		// Keep the last read's too, for a read that came back empty.
		if len(now) > 0 {
			seen = now
		}
		after = log.Next
	}
}

func logFailed(ctx context.Context, err error, slug string, c *api.Client, stderr io.Writer) int {
	if ctx.Err() != nil {
		return 130
	}
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == "no_such_pony" {
		fmt.Fprintf(stderr, "pony: no pony %q that this token reaches (pony list shows them)\n", slug)
		return ExitNotFound
	}
	return failed(err, c, stderr)
}
