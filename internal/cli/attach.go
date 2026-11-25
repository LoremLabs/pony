package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

// pony attach [<pony>]: share this computer with a pony, working in this
// folder. txco does it: `txco attach <server>?pony=<pony>` opens the site's
// /attach page, where you confirm and the pony is already chosen, then
// serves this computer until stopped (OnePony docs/0086 §6). pony only finds
// txco and hands the terminal to it.

// For tests.
var (
	lookTxco = func() (string, error) { return exec.LookPath("txco") }
	// txcoTakesSite says whether this txco can attach to a site, by its
	// usage: `txco attach <code>` came first.
	txcoTakesSite = func(path string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, path, "attach", "-h")
		cmd.Stdout, cmd.Stderr = &out, &out
		_ = cmd.Run()
		return strings.Contains(out.String(), "<site>")
	}
	// handTo runs txco in pony's place (attach_unix.go, attach_windows.go).
	handTo = execTxco
)

const txcoInstall = "brew install loremlabs/txco/txco"

func runAttach(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony attach [<pony>]\n\nShare this computer with a pony, working in this folder. You confirm in your\nbrowser. Needs txco.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	pos, code, ok := parse(fs, args, 1)
	if !ok {
		return code
	}
	if len(pos) == 1 && !slugRE.MatchString(pos[0]) {
		fmt.Fprintf(stderr, "pony: %q is not a pony (pony list shows them)\n", pos[0])
		return ExitUsage
	}
	if *server == "" {
		*server = getenv("ONEPONY_SERVER")
	}
	c, err := api.New(*server, "", b.Version)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitUsage
	}
	path, err := lookTxco()
	if err != nil {
		fmt.Fprintf(stderr, "pony: attach needs txco. Install it:\n\n  %s\n\n", txcoInstall)
		return ExitError
	}
	if !txcoTakesSite(path) {
		fmt.Fprintf(stderr, "pony: %s is too old to attach. Update it:\n\n  brew upgrade txco\n\n", path)
		return ExitError
	}
	site := c.Server
	if len(pos) == 1 {
		site += "?" + url.Values{"pony": {pos[0]}}.Encode()
	}
	return handTo(path, []string{"txco", "attach", site}, stderr)
}
