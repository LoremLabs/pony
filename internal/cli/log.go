package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

const (
	logDefault = 20
	logMost    = 100 // what the server gives at a time
)

// logTime is when a line happened: ISO 8601, in UTC.
func logTime(at string) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return at
	}
	return t.UTC().Format(time.RFC3339)
}

// What a line says about itself, from the terminal's own sixteen colours, so
// they suit its background.
const (
	bold   = "1"
	dim    = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
)

// ponyColours are what a pony's name can be: mid-tones of the 256 colours, to
// read on dark and light backgrounds alike, and none of the red, yellow and
// green that say how a line went. Each comes plain and bold, so fewer ponies
// share one. A name's colour goes by the length of this list, so changing it
// at all recolours every pony.
var ponyColours = []string{
	"38;5;33",  // azure
	"38;5;37",  // teal
	"38;5;61",  // slate
	"38;5;98",  // purple
	"38;5;134", // orchid
	"38;5;139", // mauve
	"38;5;168", // rose
	"1;38;5;33",
	"1;38;5;37",
	"1;38;5;61",
	"1;38;5;98",
	"1;38;5;134",
	"1;38;5;139",
	"1;38;5;168",
}

// ponyColour is the same for a pony every time, on every machine.
func ponyColour(pony string) string {
	return ponyColours[crc32.ChecksumIEEE([]byte(pony))%uint32(len(ponyColours))]
}

// kindColour is for the lines worth picking out: "" for the rest.
func kindColour(kind string) string {
	switch kind {
	case "abandoned", "unsent", "file_failed":
		return red
	case "waiting", "deferred":
		return yellow
	case "done":
		return green
	}
	return ""
}

// kindMark says at a glance how a line went: " " for most, which say nothing.
func kindMark(kind string) string {
	switch kind {
	case "abandoned", "unsent", "file_failed":
		return "✗"
	case "waiting", "deferred":
		return "…"
	case "done":
		return "✓"
	}
	return " "
}

// paint wraps s in a colour; nothing at all when colour is off.
func paint(on bool, colour, s string) string {
	if !on || colour == "" || s == "" {
		return s
	}
	return "\x1b[" + colour + "m" + s + "\x1b[0m"
}

// cellsOf is how many columns s takes. Close enough to keep a line from
// wrapping: everything from the CJK blocks up, emoji with them, takes two.
func cellsOf(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x2E80 {
			n++
		}
	}
	return n
}

// fit cuts s to n columns, with an ellipsis if anything went.
func fit(s string, n int) string {
	if cellsOf(s) <= n {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := cellsOf(string(r))
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return strings.TrimRight(b.String(), " ") + "…"
}

// mostName is the widest a name is padded to: past it, a line runs long
// rather than pushing every other line across the screen.
const mostName = 24

// logView is how lines are shown. To a program, or a pipe, each is a whole
// record: its time, who, and everything it says, so grep finds it. On a
// terminal they're for reading: one column for the names and one for how a
// line went, a card's title once rather than on every line, a space between
// cards, and nothing wider than the screen.
type logView struct {
	w      io.Writer
	pretty bool
	colour bool
	debug  bool
	names  int    // the column the names are padded to
	any    bool   // a line has been shown
	card   string // the last line's card
	titled string // the card whose title was shown last
}

func newLogView(w io.Writer, debug bool) *logView {
	pretty := isTerminal(w) && getenv("TERM") != "dumb" && runtime.GOOS != "windows"
	return &logView{w: w, debug: debug, pretty: pretty, colour: pretty && getenv("NO_COLOR") == ""}
}

// measure makes room for the names about to be shown.
func (v *logView) measure(events []api.Event) {
	for _, e := range events {
		v.names = max(v.names, min(cellsOf(e.Who), mostName))
	}
}

// words is what a line says, and the card's title after it if it comes last.
// A card's title shows once, on the first line that names it; after that a
// line says "it", until the card changes.
func (v *logView) words(e api.Event) (text, after string) {
	text = e.Text
	if e.Title == "" {
		return text, ""
	}
	if e.Kind == "said" || e.Kind == "message" && !strings.HasSuffix(text, " · "+e.Title) {
		// A chat's title is its first line: said here, it needs no saying again.
		if strings.Contains(text, strings.TrimSuffix(e.Title, "…")) {
			v.titled = e.Card
		}
		return text, ""
	}
	seen := v.titled == e.Card
	v.titled = e.Card
	if suffix := " · " + e.Title; strings.HasSuffix(text, suffix) {
		text = strings.TrimSuffix(text, suffix)
		if seen {
			return text, ""
		}
		return text, suffix
	}
	if quoted := "“" + e.Title + "”"; seen {
		return strings.Replace(text, quoted, "it", 1), ""
	}
	return text, ""
}

// ids are a line's run and request, for --debug: "" when it has neither.
func logIDs(e api.Event) string {
	var ids []string
	if e.Run != "" {
		ids = append(ids, "run="+e.Run)
	}
	if e.RID != "" {
		ids = append(ids, "rid="+e.RID)
	}
	return strings.Join(ids, " ")
}

// line is one line of the log, with what comes before it.
func (v *logView) line(e api.Event) string {
	ids := ""
	if v.debug {
		ids = logIDs(e)
	}
	if !v.pretty {
		line := logTime(e.At) + "  " + e.Who + " " + e.Text
		if ids != "" {
			line += "  " + ids
		}
		return line
	}

	gap := ""
	if v.any && e.Card != v.card {
		gap = "\n"
	}
	if e.Card != v.card {
		v.titled = "" // back to a card seen before, it says which again
	}
	v.any, v.card = true, e.Card
	v.measure([]api.Event{e})

	who := e.Who
	if cellsOf(who) < v.names {
		who += strings.Repeat(" ", v.names-cellsOf(who))
	}
	name := paint(v.colour, bold, who)
	if e.Who == e.Pony {
		name = paint(v.colour, ponyColour(e.Pony), who)
	}
	when := logTime(e.At)
	text, after := v.words(e)

	// Nothing wider than the screen, leaving the last column alone: a line
	// that fills it wraps on some terminals. --debug's ids are why it's on.
	if cols, _ := termSize(v.w); cols > 0 {
		room := cols - 1 - cellsOf(when) - 2 - cellsOf(who) - 2 - 2
		if ids != "" {
			room -= 2 + cellsOf(ids)
		}
		if room > 8 {
			if cellsOf(text) >= room {
				text, after = fit(text, room), ""
			} else {
				after = fit(after, room-cellsOf(text))
			}
		}
	}

	colour := kindColour(e.Kind)
	line := paint(v.colour, dim, when) + "  " + name + "  " + paint(v.colour, colour, kindMark(e.Kind)) + " " +
		paint(v.colour, colour, text) + paint(v.colour, dim, after)
	if ids != "" {
		line += "  " + paint(v.colour, dim, ids)
	}
	return gap + line
}

func runLog(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var follow bool
	fs.BoolVar(&follow, "f", false, "keep printing what happens, until Ctrl-C")
	fs.BoolVar(&follow, "follow", false, "the same as -f")
	lines := fs.Int("n", logDefault, "how many lines to show, up to 100")
	asJSON := fs.Bool("json", false, "print the lines as JSON (with -f, NDJSON: one object per line)")
	debug := fs.Bool("debug", false, "show each line's run, and the request that held it")
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
	view := newLogView(stdout, *debug)
	show := func(e api.Event) {
		if *asJSON {
			printJSON(stdout, stderr, e)
			return
		}
		fmt.Fprintln(stdout, view.line(e))
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
	shown := log.Events[max(0, len(log.Events)-*lines):]
	view.measure(shown)
	for _, e := range shown {
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
