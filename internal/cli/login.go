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

// answerWait is how long the browser's redirect waits for the exchange,
// to say how it went.
var answerWait = 30 * time.Second

// haveBrowser says whether a browser opened here can reach this terminal's
// listener: not over ssh (it would open on the far machine, or nowhere),
// and on Linux and the BSDs only with a display. $BROWSER set says yes.
// Tests replace it.
var haveBrowser = func() bool {
	if strings.TrimSpace(getenv("BROWSER")) != "" {
		return true
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// computerName is the name a Mac goes by (System Settings, General, About).
// Its host name won't do: with none set, that's whatever the router handed
// out. Elsewhere there is no such name. Tests replace it.
var computerName = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("/usr/sbin/scutil", "--get", "ComputerName").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// deviceName is what the token is named for: this machine.
func deviceName() string {
	host, _ := os.Hostname()
	return machineName(computerName(), host)
}

// machineName is the computer's name, else its host name without the local
// network's suffix, in 40 characters at most.
func machineName(computer, host string) string {
	name := strings.TrimSpace(computer)
	if name == "" {
		name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(host), ".local"), ".lan")
	}
	if r := []rune(name); len(r) > 40 {
		name = strings.TrimSpace(string(r[:40]))
	}
	return name
}

func runLogin(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noBrowser := fs.Bool("no-browser", false, "don't open a browser here: print the address, and read the code it shows")
	name := fs.String("name", "", "what to call this machine on your Account page (default: its name)")
	server := fs.String("server", "", serverHelp)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: pony login [--no-browser] [--name NAME]

Sign in to OnePony in your browser, and save a token for this machine. It
reaches every pony for 90 days. Revoke it on your Account page, or with
'pony logout'.

Without a browser here (--no-browser, or over ssh), open the address it
prints in any browser and paste the code that page shows.

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
	// The authorize page, sending the browser back to redirect (none: the
	// page shows a code to paste).
	authorize := func(redirect string) string {
		q := url.Values{"challenge": {pk.Challenge}, "method": {"S256"}, "state": {state}}
		if redirect != "" {
			q.Set("redirect_uri", redirect)
		}
		if device != "" {
			q.Set("name", device)
		}
		return c.Server + "/cli/authorize?" + q.Encode()
	}

	// The browser on this machine comes back to a listener here, on a port
	// of its own; anywhere, a code pasted from the page does the same.
	var (
		redirect string
		codes    chan callback
	)
	if !*noBrowser && haveBrowser() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
			codes = make(chan callback, 1)
			srv := &http.Server{Handler: callbackHandler(state, c.Server+"/cli/done", codes), ReadHeaderTimeout: 10 * time.Second}
			go srv.Serve(ln)
			// Shutdown, not Close: the browser's redirect is written first.
			defer func() {
				sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				srv.Shutdown(sctx)
			}()
		}
	}
	if redirect != "" {
		fmt.Fprintln(stderr, "Opening your browser to sign in…")
		if err := openBrowser(authorize(redirect)); err != nil {
			fmt.Fprintf(stderr, "pony: couldn't open a browser (%v)\n", err)
		}
		fmt.Fprintf(stderr, "If it didn't open, go to this address and paste the code it shows:\n\n  %s\n\n", authorize(""))
	} else {
		fmt.Fprintf(stderr, "Go to this address in a browser and paste the code it shows:\n\n  %s\n\n", authorize(""))
	}
	fmt.Fprint(stderr, "Code: ")
	pasted := readCodes(stdin)

	// What came back: the code, the redirect it was granted for ("" for a
	// pasted one), and, from the browser, where to tell it how it went.
	var (
		code, via string
		answer    = func(string) {}
	)
	for code == "" {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintln(stderr, "pony: timed out; nothing was signed in")
				return ExitTimeout
			}
			fmt.Fprintln(stderr, "pony: stopped; nothing was signed in")
			return 130
		case cb := <-codes:
			fmt.Fprintln(stderr)
			if cb.err != "" {
				fmt.Fprintln(stderr, "pony: cancelled in the browser; nothing was signed in")
				return ExitAuth
			}
			code, via = cb.code, redirect
			var once sync.Once
			answer = func(result string) { once.Do(func() { cb.answer <- result }) }
			defer answer("failed")
		case line, ok := <-pasted:
			if !ok {
				pasted = nil
				if codes == nil {
					fmt.Fprintln(stderr, "\npony: no code; nothing was signed in")
					return ExitAuth
				}
				continue
			}
			if !strings.HasPrefix(line, "pcode_") {
				fmt.Fprint(stderr, "That isn't the code the page shows (it starts with pcode_). Code: ")
				continue
			}
			code, via = line, ""
		}
	}

	g, err := c.Exchange(ctx, code, pk.Verifier, via)
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
	answer("ok")
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

// callback is the browser's redirect: a code, or a refusal; and where to
// say how the sign-in went (ok or failed), to send the browser on.
type callback struct {
	code, err string
	answer    chan<- string
}

// callbackHandler takes the browser's one redirect, with the state this
// sign-in sent, and sends the browser on to the site's done page
// (done?result=ok, cancelled or failed): once the terminal says how the
// exchange went, or answerWait.
func callbackHandler(state, done string, out chan<- callback) http.Handler {
	var mu sync.Mutex
	taken := false
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
		w.Header().Set("Cache-Control", "no-store")
		mu.Lock()
		first := !taken
		taken = true
		mu.Unlock()
		if !first {
			http.Redirect(w, r, done, http.StatusFound)
			return
		}
		if q.Get("error") != "" {
			out <- callback{err: q.Get("error")}
			http.Redirect(w, r, done+"?result=cancelled", http.StatusFound)
			return
		}
		answer := make(chan string, 1)
		out <- callback{code: q.Get("code"), answer: answer}
		result := "failed"
		select {
		case result = <-answer:
		case <-time.After(answerWait):
		case <-r.Context().Done():
			return
		}
		http.Redirect(w, r, done+"?result="+result, http.StatusFound)
	})
}

// readCodes reads lines from r (a code the page shows, pasted), trimmed,
// the empty ones skipped; the channel closes at the end of r.
func readCodes(r io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				out <- s
			}
		}
	}()
	return out
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
