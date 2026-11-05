package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/loremlabs/pony/internal/api"
)

// stdin is os.Stdin; tests replace it.
var stdin io.Reader = os.Stdin

// Polling: quick at first, while an answer is most likely, then slower.
// Tests shorten them.
var (
	pollFast  = time.Second
	pollSlow  = 3 * time.Second
	fastFor   = 20 * time.Second
	settleFor = 3 // reads of a settled card with no reply before giving up on one
)

// maxLine is the longest line a pony takes, in characters (OnePony's own
// limit, the web chat's); maxStdin bounds what `-` reads before counting.
const (
	maxLine  = 4000
	maxStdin = 64 << 10
)

// interrupted is a context that ends on Ctrl-C, or when something else asks
// the command to stop. It stops the waiting, never the pony, and it's how the
// drawing gets to clean up and give the cursor back.
func interrupted() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// readLine is what to say: the words after the pony, or stdin for "-".
func readLine(words []string, stderr io.Writer) (string, int) {
	var text string
	if len(words) == 1 && words[0] == "-" {
		b, err := io.ReadAll(io.LimitReader(stdin, maxStdin+1))
		if err != nil {
			fmt.Fprintf(stderr, "pony: reading stdin: %v\n", err)
			return "", ExitError
		}
		if len(b) > maxStdin {
			fmt.Fprintf(stderr, "pony: stdin is over %d KiB; a line to a pony is %d characters at most\n", maxStdin>>10, maxLine)
			return "", ExitUsage
		}
		if !utf8.Valid(b) {
			fmt.Fprintln(stderr, "pony: stdin isn't UTF-8 text")
			return "", ExitUsage
		}
		text = string(b)
	} else {
		text = strings.Join(words, " ")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		fmt.Fprintln(stderr, "pony: nothing to say (give the words, or - to read them from stdin)")
		return "", ExitUsage
	}
	if n := utf8.RuneCountInString(text); n > maxLine {
		fmt.Fprintf(stderr, "pony: that's %d characters; a line to a pony is %d at most\n", n, maxLine)
		return "", ExitUsage
	}
	return text, ExitOK
}

// follower reads a card until its pony is done with it, handing on each
// line once.
type follower struct {
	c     *api.Client
	card  string
	after int
	state string
	pony  string // the card's pony, as of the read whose lines are being handed on
	// line is called for each line after the cursor, in order; state for
	// each change of state (and the first).
	line  func(api.Line)
	onSet func(string)
}

// errNoAnswer: the card never answered (the network, or the server, kept
// failing).
var errNoAnswer = errors.New("no answer")

// run follows until the pony is not busy with the card, then returns its
// state. until, when set, is also asked after each read: true ends it.
func (f *follower) run(ctx context.Context, until func(*api.Card) bool) (string, error) {
	start := time.Now()
	failures := 0
	for {
		r, err := f.c.ReadCard(ctx, f.card, f.after)
		if err != nil {
			if ctx.Err() != nil {
				return f.state, ctx.Err()
			}
			var ae *api.Error
			if errors.As(err, &ae) && ae.Status != 502 && ae.Status != 503 && ae.Status != 504 {
				return f.state, err // not signed in, not found: reading again won't help
			}
			if failures++; failures > 5 {
				return f.state, err
			}
		} else {
			failures = 0
			f.pony = r.Card.Pony
			for _, l := range r.Lines {
				if l.Seq > f.after {
					f.line(l)
				}
			}
			f.after = r.Next
			if r.Card.State != f.state {
				f.state = r.Card.State
				if f.onSet != nil {
					f.onSet(f.state)
				}
			}
			if until(r) {
				return f.state, nil
			}
		}
		wait := pollFast
		if time.Since(start) > fastFor {
			wait = pollSlow
		}
		select {
		case <-ctx.Done():
			return f.state, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// settled is how a finished card's state ends a follow: its exit code and
// what to say on stderr ("" for nothing).
func settled(state, card string, c *api.Client) (int, string) {
	switch state {
	case "needs_you":
		return ExitWaiting, fmt.Sprintf("waiting on you: answer or approve it on the board (%s/board?card=%s), then: pony watch %s", c.Server, card, card)
	case "waiting":
		return ExitOK, fmt.Sprintf("waiting on someone else's answer; to follow it: pony watch %s", card)
	case "failed":
		return ExitFailed, fmt.Sprintf("the pony's run failed; the card is %s", card)
	}
	return ExitOK, ""
}

func runAsk(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the answer as one JSON value")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for the answer (0: no limit)")
	card := fs.String("card", "", "carry on the chat on this card, instead of starting one")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: pony ask [flags] <pony> <words…|->

Say something to a pony and wait for its answer. Each ask starts a chat of
its own (a card on the pony's board); --card carries on one. Give - to read
the words from stdin (up to 4000 characters).

The answer is printed as it comes. If the pony needs you first (a mail to
approve, a question), pony says so and exits 5; Ctrl-C stops the waiting,
not the pony. Either way 'pony watch <card>' follows it from there.

Flags:
`)
		fs.PrintDefaults()
	}
	pos, code, ok := parse(fs, args, -1)
	if !ok {
		return code
	}
	if len(pos) < 2 {
		fs.Usage()
		return ExitUsage
	}
	slug := pos[0]
	if *card != "" && !validCard(*card) {
		fmt.Fprintf(stderr, "pony: %q isn't a card id\n", *card)
		return ExitUsage
	}
	text, code := readLine(pos[1:], stderr)
	if code != ExitOK {
		return code
	}
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}

	ctx, stop := interrupted()
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	on := *card
	if on == "" {
		on = api.NewCard()
	}
	wait := startWaiting(stderr, *asJSON, "sending to "+slug)
	defer wait.stop()
	said, err := c.Say(ctx, slug, text, on, api.NewKey())
	if err != nil {
		wait.stop()
		if ctx.Err() != nil {
			return gaveUp(ctx, stderr, "", "sending")
		}
		var ae *api.Error
		if errors.As(err, &ae) && ae.Code == "pony_waiting" {
			fmt.Fprintf(stderr, "pony: %s is still getting ready; it takes lines once it's let in (its mail is held for it meanwhile)\n", slug)
			return ExitWaiting
		}
		if errors.As(err, &ae) && ae.Code == "no_such_pony" {
			fmt.Fprintf(stderr, "pony: no pony %q that this token reaches (pony list shows them)\n", slug)
			return ExitNotFound
		}
		return failed(err, c, stderr)
	}

	var answer []api.Line
	sinceReply := 0
	f := &follower{c: c, card: said.Card, after: said.Seq,
		line: func(l api.Line) {
			if l.From != "pony" {
				return // someone typing on the same chat in the browser: theirs to see there
			}
			answer = append(answer, l)
			if !*asJSON {
				wait.print(func() { fmt.Fprintln(stdout, l.Text) })
			}
		},
		onSet: func(s string) { wait.say(busyWords(slug, s)) },
	}
	state, err := f.run(ctx, func(r *api.Card) bool {
		if api.Busy(r.Card.State) {
			sinceReply = 0
			return false
		}
		// Done with it: the answer is in, or the card has stayed done a
		// few reads with none (the reply lands just after the run ends).
		if len(answer) > 0 || r.Card.State != "idle" {
			return true
		}
		sinceReply++
		return sinceReply >= settleFor
	})
	wait.stop()
	if err != nil {
		if ctx.Err() != nil {
			return gaveUp(ctx, stderr, said.Card, "waiting")
		}
		return failed(err, c, stderr)
	}
	code, note := settled(state, said.Card, c)
	if state == "idle" && len(answer) == 0 {
		note = fmt.Sprintf("%s finished without a reply; the card is %s", slug, said.Card)
	}
	if *asJSON {
		if answer == nil {
			answer = []api.Line{}
		}
		if c := printJSON(stdout, stderr, map[string]any{"pony": slug, "card": said.Card, "state": state, "lines": answer}); c != ExitOK {
			return c
		}
	}
	if note != "" {
		fmt.Fprintf(stderr, "pony: %s\n", note)
	}
	return code
}

// gaveUp reports a wait that ended before the pony did: Ctrl-C or
// --timeout. The pony carries on either way.
func gaveUp(ctx context.Context, stderr io.Writer, card, doing string) int {
	follow := ""
	if card != "" {
		follow = "; to follow it: pony watch " + card
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "pony: timed out %s (the pony carries on%s)\n", doing, follow)
		return ExitTimeout
	}
	fmt.Fprintf(stderr, "pony: stopped %s (the pony carries on%s)\n", doing, follow)
	return 130
}

func validCard(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func runWatch(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print NDJSON: a line per thing said, and each change of state")
	after := fs.Int("after", 0, "only what was said after this line (the seq --json gives)")
	timeout := fs.Duration("timeout", 0, "how long to follow (0: until the pony is done with it)")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: pony watch [flags] <card>

Follow a card (the chat an ask started, or one a pony works on): what was
said on it, then each new line, until the pony is done with it. Exits 0
when it is, 5 when it waits on you, 7 when its run failed.

Flags:
`)
		fs.PrintDefaults()
	}
	pos, code, ok := parse(fs, args, 1)
	if !ok {
		return code
	}
	if len(pos) != 1 || !validCard(pos[0]) {
		fs.Usage()
		return ExitUsage
	}
	card := pos[0]
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}
	ctx, stop := interrupted()
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	emit := func(v any) {
		printJSON(stdout, stderr, v)
	}
	wait := startWaiting(stderr, *asJSON, "following")
	defer wait.stop()
	var f *follower
	f = &follower{c: c, card: card, after: *after,
		line: func(l api.Line) {
			if *asJSON {
				emit(map[string]any{"type": "line", "seq": l.Seq, "from": l.From, "text": l.Text, "at": l.At, "via": l.Via})
				return
			}
			who := "you"
			if l.From == "pony" {
				who = f.pony
			}
			wait.print(func() { fmt.Fprintf(stdout, "%s: %s\n", who, l.Text) })
		},
		onSet: func(s string) {
			switch {
			case *asJSON:
				emit(map[string]any{"type": "state", "card": card, "state": s})
			case wait != nil:
				wait.say(busyWords(f.pony, s))
			case s == "working" || s == "queued":
				fmt.Fprintf(stderr, "pony: %s is %s\n", f.pony, map[string]string{"working": "working on it", "queued": "busy; this card is next"}[s])
			}
		},
	}
	state, err := f.run(ctx, func(r *api.Card) bool { return !api.Busy(r.Card.State) })
	wait.stop()
	if err != nil {
		if ctx.Err() != nil {
			return gaveUp(ctx, stderr, "", "watching")
		}
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			fmt.Fprintf(stderr, "pony: no card %q of a pony this token reaches\n", card)
			return ExitNotFound
		}
		return failed(err, c, stderr)
	}
	code, note := settled(state, card, c)
	if note != "" && !*asJSON {
		fmt.Fprintf(stderr, "pony: %s\n", note)
	}
	return code
}

func runStatus(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the pony as JSON")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony status [--json] <pony>\n\nWhat one pony is doing, and the card it is on.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	pos, code, ok := parse(fs, args, 1)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fs.Usage()
		return ExitUsage
	}
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}
	p, err := c.Pony(context.Background(), pos[0])
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			fmt.Fprintf(stderr, "pony: no pony %q that this token reaches (pony list shows them)\n", pos[0])
			return ExitNotFound
		}
		return failed(err, c, stderr)
	}
	if *asJSON {
		return printJSON(stdout, stderr, p)
	}
	who := p.Slug
	if p.Name != "" {
		who = p.Name
	}
	if p.Address != "" {
		who += " <" + p.Address + ">"
	}
	fmt.Fprintln(stdout, who)
	status := statusWords[p.Status]
	if status == "" {
		status = p.Status
	}
	if !p.Ready {
		status = "getting ready"
	}
	if p.Doing != "" {
		status += ": " + p.Doing
	}
	fmt.Fprintln(stdout, status)
	if p.Card != "" {
		fmt.Fprintf(stdout, "card %s (pony watch %s)\n", p.Card, p.Card)
	}
	return ExitOK
}
