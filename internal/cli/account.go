package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/auth"
)

// getenv is os.Getenv; tests replace it.
var getenv = os.Getenv

const accountPage = "/settings/account"

// connect builds the API client from --server (else ONEPONY_SERVER, else
// the default) and a token: ONEPONY_TOKEN when it is set (scripts, CI),
// else the one `pony login` saved for that server. A server other than the
// default is named on stderr, so a script's output says where it talked.
func connect(server string, b Build, stderr io.Writer) (*api.Client, int) {
	if server == "" {
		server = getenv("ONEPONY_SERVER")
	}
	c, err := api.New(server, "", b.Version)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return nil, ExitUsage
	}
	if c.Server != api.DefaultServer {
		fmt.Fprintf(stderr, "pony: using %s\n", c.Server)
	}
	token := strings.TrimSpace(getenv("ONEPONY_TOKEN"))
	if token != "" {
		if !api.ValidToken(token) {
			fmt.Fprintf(stderr, "pony: ONEPONY_TOKEN isn't a pony token (they start with pony_). Copy it again from %s%s.\n", c.Server, accountPage)
			return nil, ExitAuth
		}
		c.Token = token
		return c, ExitOK
	}
	if st, err := auth.DefaultStore(getenv); err == nil {
		saved, ok, err := st.Get(c.Server)
		if err != nil {
			fmt.Fprintf(stderr, "pony: %v\n", err)
			return nil, ExitError
		}
		if ok && api.ValidToken(saved.Token) {
			c.Token = saved.Token
			return c, ExitOK
		}
	}
	fmt.Fprintf(stderr, "pony: not signed in. Run 'pony login', or set ONEPONY_TOKEN to a token from your Account page (%s%s).\n", c.Server, accountPage)
	return nil, ExitAuth
}

// failed reports an API error on stderr and returns its exit code.
func failed(err error, c *api.Client, stderr io.Writer) int {
	var ae *api.Error
	var ne *api.NetError
	switch {
	case errors.As(err, &ae):
		switch {
		case ae.Status == 401:
			fmt.Fprintf(stderr, "pony: not signed in: the token was revoked, has expired, or isn't one. Run 'pony login' again, or make another on %s%s.\n", c.Server, accountPage)
			return ExitAuth
		case ae.Status == 403:
			fmt.Fprintf(stderr, "pony: not allowed: %s\n", ae)
			return ExitAuth
		case ae.Status == 404:
			fmt.Fprintf(stderr, "pony: not found: %s\n", ae)
			return ExitNotFound
		default:
			fmt.Fprintf(stderr, "pony: %s (HTTP %d)\n", ae, ae.Status)
			return ExitError
		}
	case errors.As(err, &ne):
		fmt.Fprintf(stderr, "pony: %v\n", ne)
		return ExitTimeout
	default:
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitError
	}
}

// parse handles a command's flags, wherever they are among its arguments
// (`pony list --json` and `pony ask pepper hi --json` alike; "--" ends
// them), and returns the rest. ok false means return code now.
func parse(fs *flag.FlagSet, args []string, maxArgs int) (pos []string, code int, ok bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return nil, ExitOK, false
			}
			return nil, ExitUsage, false
		}
		rest := fs.Args()
		if n := len(args) - len(rest); len(rest) == 0 || (n > 0 && args[n-1] == "--") {
			pos = append(pos, rest...)
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	if maxArgs >= 0 && len(pos) > maxArgs {
		fs.Usage()
		return nil, ExitUsage, false
	}
	return pos, ExitOK, true
}

func printJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitError
	}
	return ExitOK
}

const serverHelp = "OnePony server (default $ONEPONY_SERVER, else " + api.DefaultServer + ")"

func runWhoami(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the answer as JSON")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony whoami [--json]\n\nShow the account your token signs in as, and what the token reaches.\nThe token is read from ONEPONY_TOKEN.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if _, code, ok := parse(fs, args, 0); !ok {
		return code
	}
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}
	me, err := c.Me(context.Background())
	if err != nil {
		return failed(err, c, stderr)
	}
	if *asJSON {
		return printJSON(stdout, stderr, me)
	}
	who := me.Address
	if me.Name != "" {
		who = fmt.Sprintf("%s <%s>", me.Name, me.Address)
	}
	fmt.Fprintln(stdout, who)
	reach := "all your ponies"
	if slugs := me.Token.Reach(); slugs != nil {
		reach = strings.Join(slugs, ", ")
	}
	expires := me.Token.Expires
	if len(expires) >= 10 {
		expires = expires[:10]
	}
	fmt.Fprintf(stdout, "token %q reaches %s, until %s\n", me.Token.Name, reach, expires)
	return ExitOK
}

// statusWords is how list says a pony's status.
var statusWords = map[string]string{
	"working":   "working",
	"needs_you": "needs you",
	"waiting":   "waiting",
	"available": "available",
}

func runList(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the ponies as a JSON array")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony list [--json]\n\nList the ponies your token reaches, and what each is doing.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if _, code, ok := parse(fs, args, 0); !ok {
		return code
	}
	c, code := connect(*server, b, stderr)
	if c == nil {
		return code
	}
	ponies, err := c.Ponies(context.Background())
	if err != nil {
		return failed(err, c, stderr)
	}
	if *asJSON {
		return printJSON(stdout, stderr, ponies)
	}
	if len(ponies) == 0 {
		fmt.Fprintf(stderr, "pony: no ponies here yet. Make one at %s/new\n", c.Server)
		return ExitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PONY\tNAME\tSTATUS\tDOING")
	for _, p := range ponies {
		status := statusWords[p.Status]
		if status == "" {
			status = p.Status
		}
		if !p.Ready {
			status = "getting ready"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Slug, p.Name, status, p.Doing)
	}
	tw.Flush()
	return ExitOK
}
