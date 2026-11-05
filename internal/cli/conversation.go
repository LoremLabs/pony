package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"golang.org/x/term"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/auth"
	"github.com/loremlabs/pony/internal/tui"
)

// slugRE is a pony's slug, as the server has it.
var slugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]|\.[a-z0-9]){0,62}$`)

// stdinIsTerminal says whether someone is there to type; tests replace it.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// interactive is whether this is a person at a terminal: both ends of it,
// since a script or a pipe must never be left waiting on a screen.
func interactive(stdout io.Writer) bool {
	return stdinIsTerminal() && isTerminal(stdout) && getenv("TERM") != "dumb" && getenv("CI") == ""
}

// conversation is tui.Run; tests replace it.
var conversation = tui.Run

// openInBrowser opens a page, or says it can't: over ssh it would open on the
// far machine, or nowhere.
func openInBrowser(url string) error {
	if !haveBrowser() {
		return errors.New("no browser here")
	}
	return openBrowser(url)
}

// runConversation opens a conversation, with slug if it's given. Nothing that
// can be said on the terminal waits for the screen: not signed in, or a pony
// that isn't one, are said here as any command would say them.
func runConversation(slug string, b Build, stdout, stderr io.Writer) int {
	c, code := connect("", b, stderr)
	if c == nil {
		return code
	}
	ctx, stop := interrupted()
	defer stop()
	me, err := c.Me(ctx)
	if err != nil {
		return failed(err, c, stderr)
	}
	if slug != "" {
		if _, err := c.Pony(ctx, slug); err != nil {
			var ae *api.Error
			if errors.As(err, &ae) && ae.Status == 404 {
				return unknownCommand(slug, stderr) // a typo shouldn't open a screen
			}
			return failed(err, c, stderr)
		}
	}
	prefs, err := auth.DefaultPrefs(getenv)
	if err != nil {
		prefs = nil
	}
	res := conversation(ctx, tui.Options{Client: c, Address: me.Address, Pony: slug, Prefs: prefs, Plain: getenv("NO_COLOR") != "", Open: openInBrowser})
	if res.Err != nil {
		var ae *api.Error
		if errors.As(res.Err, &ae) {
			return failed(res.Err, c, stderr)
		}
		fmt.Fprintf(stderr, "pony: %v\n", res.Err)
		return ExitError
	}
	switch {
	case res.Pony == "":
	case res.Busy:
		fmt.Fprintf(stdout, "Left %s; it carries on. To follow it: pony watch %s\n", res.Pony, res.Card)
	default:
		fmt.Fprintf(stdout, "Left %s; it carries on.\n", res.Pony)
	}
	return ExitOK
}
