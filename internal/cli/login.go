package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/auth"
)

// openBrowser opens u in the person's browser: $BROWSER when it is set,
// else the system's opener. Tests replace it.
var openBrowser = func(u string) error {
	if b := strings.Fields(getenv("BROWSER")); len(b) > 0 {
		return exec.Command(b[0], append(b[1:], u)...).Start()
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// loginWait is how long a sign-in may take in the browser.
var loginWait = 10 * time.Minute

const callbackPage = `<!doctype html><meta charset="utf-8"><title>pony</title>
<style>body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:32rem;padding:0 1rem}</style>
<p>%s</p>`

// deviceName is what the token is named for: this machine.
func deviceName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h = strings.TrimSuffix(strings.TrimSuffix(h, ".local"), ".lan")
	if len(h) > 40 {
		h = h[:40]
	}
	return h
}

func runLogin(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noBrowser := fs.Bool("no-browser", false, "don't open a browser here: print the address, and read the code it shows")
	name := fs.String("name", "", "what to call this machine on your Account page (default: its host name)")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: pony login [--no-browser] [--name NAME]

Sign in to OnePony in your browser. The page asks you to let this terminal
use OnePony as you; once you allow it, pony saves a token (every pony, 90
days) in its credentials file, readable by you alone. It's listed on your
Account page, where you can revoke it; 'pony logout' revokes it too.

On a machine without a browser (over ssh), --no-browser prints the address
to open anywhere, and reads the code that page shows.

Flags:
`)
		fs.PrintDefaults()
	}
	if _, code, ok := parse(fs, args, 0); !ok {
		return code
	}
	if *server == "" {
		*server = getenv("ONEPONY_SERVER")
	}
	c, err := api.New(*server, "", b.Version)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitUsage
	}
	if c.Server != api.DefaultServer {
		fmt.Fprintf(stderr, "pony: using %s\n", c.Server)
	}
	st, err := auth.DefaultStore(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pony: nowhere to keep the token: %v\n", err)
		return ExitError
	}
	if strings.TrimSpace(getenv("ONEPONY_TOKEN")) != "" {
		fmt.Fprintln(stderr, "pony: note: ONEPONY_TOKEN is set, and pony uses it before the token login saves")
	}

	ctx, stop := interrupted()
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, loginWait)
	defer cancel()

	pk := auth.NewPKCE()
	state := auth.NewState()
	device := strings.TrimSpace(*name)
	if device == "" {
		device = deviceName()
	}

	var (
		redirect string
		codes    chan callback
		srv      *http.Server
	)
	if !*noBrowser {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintf(stderr, "pony: can't listen for the browser here (%v); try pony login --no-browser\n", err)
			return ExitError
		}
		redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
		codes = make(chan callback, 1)
		srv = &http.Server{Handler: callbackHandler(state, codes), ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		defer srv.Close()
	}

	q := url.Values{"challenge": {pk.Challenge}, "method": {"S256"}, "state": {state}}
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	if device != "" {
		q.Set("name", device)
	}
	page := c.Server + "/cli/authorize?" + q.Encode()

	var code string
	if *noBrowser {
		fmt.Fprintf(stderr, "Open this address in a browser, sign in, and allow pony:\n\n  %s\n\n", page)
		fmt.Fprint(stderr, "Then paste the code it shows: ")
		line, err := readCode(ctx)
		if err != nil {
			return gaveUp(ctx, stderr, "", "signing in")
		}
		code = line
	} else {
		fmt.Fprintf(stderr, "Opening your browser to sign in. If it doesn't open, go to:\n\n  %s\n\n", page)
		if err := openBrowser(page); err != nil {
			fmt.Fprintf(stderr, "pony: couldn't open a browser (%v); open the address above\n", err)
		}
		fmt.Fprintln(stderr, "Waiting for you to allow it…")
		select {
		case <-ctx.Done():
			return gaveUp(ctx, stderr, "", "waiting for the browser")
		case cb := <-codes:
			if cb.err != "" {
				fmt.Fprintln(stderr, "pony: nothing was signed in (you cancelled it in the browser)")
				return ExitAuth
			}
			code = cb.code
		}
	}
	if !strings.HasPrefix(code, "pcode_") {
		fmt.Fprintln(stderr, "pony: that isn't the code the page shows (it starts with pcode_)")
		return ExitUsage
	}

	g, err := c.Exchange(ctx, code, pk.Verifier, redirect)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && (ae.Code == "login_gone" || ae.Code == "login_bad") {
			fmt.Fprintf(stderr, "pony: %s\n", ae)
			return ExitAuth
		}
		return failed(err, c, stderr)
	}
	c.Token = g.Secret
	saved := auth.Saved{Token: g.Secret, ID: g.Token.ID, Name: g.Token.Name, Expires: g.Token.Expires}
	me, err := c.Me(ctx)
	if err == nil {
		saved.Address = me.Address
	}
	before, had, _ := st.Get(c.Server)
	if err := st.Put(c.Server, saved); err != nil {
		fmt.Fprintf(stderr, "pony: signed in, but the token couldn't be saved (%v); revoke %q on your Account page\n", err, g.Token.Name)
		return ExitError
	}
	// The token this one replaces would otherwise live on, unused, until it
	// expired: it revokes itself (best effort: one already revoked or
	// expired answers 401, and that's the same).
	if had && before.Token != g.Secret {
		old := *c
		old.Token = before.Token
		if _, err := old.Logout(ctx); err == nil {
			fmt.Fprintf(stderr, "pony: revoked the token it replaces (%q)\n", before.Name)
		}
	}
	who := saved.Address
	if err == nil && me.Name != "" {
		who = fmt.Sprintf("%s <%s>", me.Name, me.Address)
	}
	if who == "" {
		who = "you"
	}
	expires := g.Token.Expires
	if len(expires) >= 10 {
		expires = expires[:10]
	}
	fmt.Fprintf(stdout, "Signed in as %s.\n", who)
	fmt.Fprintf(stderr, "pony: token %q, until %s, saved in %s\n", g.Token.Name, expires, st.Path)
	return ExitOK
}

type callback struct{ code, err string }

// callbackHandler takes the browser's one redirect: the code (or a refusal)
// with the state this sign-in sent, and nothing else.
func callbackHandler(state string, out chan<- callback) http.Handler {
	var mu sync.Mutex
	done := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "That isn't this sign-in. Run pony login again.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		mu.Lock()
		defer mu.Unlock()
		if done {
			fmt.Fprintf(w, callbackPage, "This sign-in is done. You can close this tab.")
			return
		}
		done = true
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, callbackPage, "Nothing was signed in. You can close this tab.")
			out <- callback{err: e}
			return
		}
		fmt.Fprintf(w, callbackPage, "Signed in. You can close this tab and go back to your terminal.")
		out <- callback{code: q.Get("code")}
	})
}

// readCode reads one line from stdin, or gives up with ctx.
func readCode(ctx context.Context) (string, error) {
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stdin).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case s := <-line:
		if s == "" {
			return "", errors.New("no code")
		}
		return s, nil
	}
}

func runLogout(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony logout\n\nSign out: revoke the token 'pony login' saved for this server, so it stops\nworking at once, and forget it here.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if _, code, ok := parse(fs, args, 0); !ok {
		return code
	}
	if *server == "" {
		*server = getenv("ONEPONY_SERVER")
	}
	c, err := api.New(*server, "", b.Version)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitUsage
	}
	if c.Server != api.DefaultServer {
		fmt.Fprintf(stderr, "pony: using %s\n", c.Server)
	}
	st, err := auth.DefaultStore(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitError
	}
	saved, ok, err := st.Get(c.Server)
	if err != nil {
		fmt.Fprintf(stderr, "pony: %v\n", err)
		return ExitError
	}
	if !ok {
		if strings.TrimSpace(getenv("ONEPONY_TOKEN")) != "" {
			fmt.Fprintf(stderr, "pony: nothing saved by pony login here. ONEPONY_TOKEN is yours to unset; revoke it on your Account page (%s%s)\n", c.Server, accountPage)
		} else {
			fmt.Fprintln(stderr, "pony: not signed in here")
		}
		return ExitOK
	}
	c.Token = saved.Token
	ctx, stop := interrupted()
	defer stop()
	if _, err := c.Logout(ctx); err != nil {
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Status != 401 {
			fmt.Fprintf(stderr, "pony: the token wasn't revoked, so it's kept here: %v. Try again, or revoke %q on your Account page.\n", err, saved.Name)
			return ExitTimeout
		}
		// 401: revoked or expired already. Nothing to revoke; forget it.
	}
	if err := st.Forget(c.Server); err != nil {
		fmt.Fprintf(stderr, "pony: revoked, but %s couldn't be updated: %v\n", st.Path, err)
		return ExitError
	}
	fmt.Fprintln(stdout, "Signed out.")
	return ExitOK
}
