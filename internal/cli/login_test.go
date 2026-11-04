package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loremlabs/pony/internal/auth"
)

// loginServer plays OnePony's side of a sign-in: the authorize page's
// grant (a code for a challenge and a redirect), the exchange (the code and
// the verifier for testToken, once), and /me and /logout for the token.
type loginServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	grants  map[string][2]string // code → {challenge, redirect}
	revoked bool
	logouts int
	bearers []string    // who called /logout
	landed  chan string // where the listener sent the browser on to
}

func newLoginServer(t *testing.T) *loginServer {
	ls := &loginServer{t: t, grants: map[string][2]string{}, landed: make(chan string, 4)}
	answer := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, status int, code string) {
		answer(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": strings.ReplaceAll(code, "_", " ")}})
	}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		switch r.URL.Path {
		case "/api/cli/token":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the exchange carries no token")
			}
			var b map[string]string
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &b)
			g, ok := ls.grants[b["code"]]
			delete(ls.grants, b["code"])
			if !ok {
				fail(w, 410, "login_gone")
				return
			}
			if auth.Challenge(b["verifier"]) != g[0] || b["redirect"] != g[1] {
				fail(w, 400, "login_bad")
				return
			}
			answer(w, 200, map[string]any{"ok": true, "secret": testToken, "token": map[string]any{"id": "tok_bbbbbbbbbbbb", "name": "pony on testbox", "ponies": "all", "expires": "2027-02-02T18:00:00Z"}})
		case "/api/cli/v1/me":
			if ls.revoked || r.Header.Get("Authorization") != "Bearer "+testToken {
				fail(w, 401, "not_signed_in")
				return
			}
			answer(w, 200, map[string]any{"ok": true, "address": "ada@example.com", "name": "Ada", "token": map[string]any{"id": "tok_bbbbbbbbbbbb", "name": "pony on testbox", "ponies": "all", "expires": "2027-02-02T18:00:00Z"}})
		case "/api/cli/v1/logout":
			ls.logouts++
			ls.bearers = append(ls.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if ls.revoked || r.Header.Get("Authorization") != "Bearer "+testToken {
				fail(w, 401, "not_signed_in")
				return
			}
			ls.revoked = true
			answer(w, 200, map[string]any{"ok": true, "revoked": "tok_bbbbbbbbbbbb"})
		default:
			fail(w, 404, "not_found")
		}
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

// grant is the authorize page's Allow: a code for this challenge and
// redirect.
func (ls *loginServer) grant(challenge, redirect string) string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	code := fmt.Sprintf("pcode_%040x", len(ls.grants)+1)
	ls.grants[code] = [2]string{challenge, redirect}
	return code
}

// aBrowser says there is a browser here, whatever the machine running the
// tests has, and leaves stdin with nothing to read.
func aBrowser(t *testing.T) {
	old, oldIn := haveBrowser, stdin
	r, _ := io.Pipe()
	haveBrowser, stdin = func() bool { return true }, r
	t.Cleanup(func() {
		haveBrowser, stdin = old, oldIn
		r.Close()
	})
}

// follow is the browser arriving at the terminal's listener; where it's
// sent on goes to landed.
func (ls *loginServer) follow(back string) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(back)
	if err != nil {
		ls.landed <- "error: " + err.Error()
		return
	}
	resp.Body.Close()
	ls.landed <- resp.Header.Get("Location")
}

// wantLanded waits for the browser to be sent to the done page with result.
func (ls *loginServer) wantLanded(t *testing.T, result string) {
	t.Helper()
	select {
	case got := <-ls.landed:
		if want := ls.srv.URL + "/cli/done?result=" + result; got != want {
			t.Fatalf("the browser was sent to %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the browser was left waiting")
	}
}

// browser stands in for the person's browser on the authorize page: allow
// grants and follows the redirect with the code; anything else cancels.
func (ls *loginServer) browser(t *testing.T, allow bool, seen *url.Values) {
	aBrowser(t)
	old := openBrowser
	openBrowser = func(page string) error {
		u, err := url.Parse(page)
		if err != nil || u.Path != "/cli/authorize" || !strings.HasPrefix(page, ls.srv.URL) {
			t.Errorf("opened %q", page)
			return err
		}
		q := u.Query()
		*seen = q
		back, _ := url.Parse(q.Get("redirect_uri"))
		v := url.Values{"state": {q.Get("state")}}
		if allow {
			v.Set("code", ls.grant(q.Get("challenge"), q.Get("redirect_uri")))
		} else {
			v.Set("error", "access_denied")
		}
		back.RawQuery = v.Encode()
		go ls.follow(back.String())
		return nil
	}
	t.Cleanup(func() { openBrowser = old })
}

func TestLogin(t *testing.T) {
	ls := newLoginServer(t)
	cfg := t.TempDir()
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg})
	var seen url.Values
	ls.browser(t, true, &seen)

	code, out, errOut := run("login", "--name", "testbox")
	if code != ExitOK || out != "Signed in as Ada <ada@example.com>.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if seen.Get("method") != "S256" || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(seen.Get("challenge")) ||
		!regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/callback$`).MatchString(seen.Get("redirect_uri")) || seen.Get("name") != "testbox" || len(seen.Get("state")) != 32 {
		t.Fatalf("the authorize page was asked %v", seen)
	}
	noToken(t, out, errOut)
	ls.wantLanded(t, "ok")

	path := filepath.Join(cfg, "pony", "credentials.json")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials %v, mode %v: want a file readable by its owner alone", err, fi)
	}
	b, _ := os.ReadFile(path)
	if !bytes.Contains(b, []byte(testToken)) || !bytes.Contains(b, []byte(ls.srv.URL)) {
		t.Fatalf("saved %s", b)
	}

	// Signed in: no ONEPONY_TOKEN needed.
	if code, out, _ := run("whoami"); code != ExitOK || !strings.HasPrefix(out, "Ada <ada@example.com>") {
		t.Fatalf("whoami after login: exit %d, %q", code, out)
	}

	// Out: revoked, and forgotten.
	if code, out, errOut := run("logout"); code != ExitOK || out != "Signed out.\n" || ls.logouts != 1 {
		t.Fatalf("logout: exit %d, %q, %q, %d calls", code, out, errOut, ls.logouts)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the credentials file outlived the last token in it: %v", err)
	}
	if code, _, errOut := run("whoami"); code != ExitAuth || !strings.Contains(errOut, "pony login") {
		t.Fatalf("after logout: exit %d, %q", code, errOut)
	}
	if code, _, errOut := run("logout"); code != ExitOK || !strings.Contains(errOut, "not signed in") {
		t.Fatalf("logout again: exit %d, %q", code, errOut)
	}
}

func TestLoginReplaces(t *testing.T) {
	ls := newLoginServer(t)
	cfg := t.TempDir()
	const older = "pony_ffffffffffffffffffffffffffffffffffffffff"
	st := &auth.Store{Path: filepath.Join(cfg, "pony", "credentials.json")}
	if err := st.Put(ls.srv.URL, auth.Saved{Token: older, ID: "tok_cccccccccccc", Name: "pony on oldbox"}); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg})
	var seen url.Values
	ls.browser(t, true, &seen)
	if code, _, errOut := run("login"); code != ExitOK {
		t.Fatalf("exit %d, %q", code, errOut)
	}
	if len(ls.bearers) != 1 || ls.bearers[0] != older {
		t.Fatalf("the replaced token wasn't asked to revoke itself: %v", ls.bearers)
	}
	if saved, ok, _ := st.Get(ls.srv.URL); !ok || saved.Token != testToken {
		t.Fatalf("saved %+v", saved)
	}
}

func TestLoginCancelled(t *testing.T) {
	ls := newLoginServer(t)
	cfg := t.TempDir()
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg})
	var seen url.Values
	ls.browser(t, false, &seen)
	if code, _, errOut := run("login"); code != ExitAuth || !strings.Contains(errOut, "cancelled") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
	ls.wantLanded(t, "cancelled")
	if _, err := os.Stat(filepath.Join(cfg, "pony", "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("a cancelled sign-in saved something: %v", err)
	}
}

func TestLoginTimesOut(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	aBrowser(t)
	old, oldWait := openBrowser, loginWait
	openBrowser = func(string) error { return nil } // the person never comes back
	loginWait = 50 * time.Millisecond
	t.Cleanup(func() { openBrowser, loginWait = old, oldWait })
	if code, _, errOut := run("login"); code != ExitTimeout || !strings.Contains(errOut, "timed out; nothing was signed in") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
}

// codeWriter watches stderr for the authorize address, and answers the
// prompt the way a person on another machine would: they open the address
// there, allow, and paste the code the page shows.
type codeWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	ls   *loginServer
	in   *io.PipeWriter
	sent bool
}

func (w *codeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if m := regexp.MustCompile(`(?m)^  (http\S+/cli/authorize\?\S+)$`).FindStringSubmatch(w.buf.String()); m != nil && !w.sent {
		w.sent = true
		u, _ := url.Parse(m[1])
		if u.Query().Has("redirect_uri") {
			w.in.CloseWithError(fmt.Errorf("--no-browser asked for a redirect"))
			return len(p), nil
		}
		code := w.ls.grant(u.Query().Get("challenge"), "")
		go fmt.Fprintln(w.in, code)
	}
	return len(p), nil
}

func TestLoginNoBrowser(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	old := openBrowser
	openBrowser = func(string) error { t.Error("--no-browser opened a browser"); return nil }
	t.Cleanup(func() { openBrowser = old })
	r, w := io.Pipe()
	oldIn := stdin
	stdin = r
	t.Cleanup(func() { stdin = oldIn })

	var o bytes.Buffer
	e := &codeWriter{ls: ls, in: w}
	code := Run([]string{"login", "--no-browser"}, testBuild, &o, e)
	if code != ExitOK || o.String() != "Signed in as Ada <ada@example.com>.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, o.String(), e.buf.String())
	}
}

// The browser opened somewhere that can't reach the listener: the address
// printed shows a code, and pasting it signs in just the same.
func TestLoginPastedWhileWaiting(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	aBrowser(t)
	old := openBrowser
	var opened string
	openBrowser = func(page string) error { opened = page; return nil }
	t.Cleanup(func() { openBrowser = old })
	r, w := io.Pipe()
	stdin = r

	var o bytes.Buffer
	e := &codeWriter{ls: ls, in: w}
	if code := Run([]string{"login"}, testBuild, &o, e); code != ExitOK || o.String() != "Signed in as Ada <ada@example.com>.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, o.String(), e.buf.String())
	}
	if !strings.Contains(opened, "redirect_uri=") {
		t.Fatalf("the browser here was opened without the listener: %q", opened)
	}
}

// No browser here (ssh): no listener, nothing opened, only the code.
func TestLoginNoBrowserHere(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	oldHave, oldOpen, oldIn := haveBrowser, openBrowser, stdin
	haveBrowser = func() bool { return false }
	openBrowser = func(string) error { t.Error("opened a browser over ssh"); return nil }
	t.Cleanup(func() { haveBrowser, openBrowser, stdin = oldHave, oldOpen, oldIn })
	r, w := io.Pipe()
	stdin = r

	var o bytes.Buffer
	e := &codeWriter{ls: ls, in: w}
	if code := Run([]string{"login"}, testBuild, &o, e); code != ExitOK {
		t.Fatalf("exit %d, stderr %q", code, e.buf.String())
	}
}

func TestLoginPaste(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	old := stdin
	t.Cleanup(func() { stdin = old })

	// Nothing to read: nothing was signed in.
	stdin = strings.NewReader("")
	if code, _, errOut := run("login", "--no-browser"); code != ExitAuth || !strings.Contains(errOut, "no code; nothing was signed in") {
		t.Fatalf("no code: exit %d, %q", code, errOut)
	}
	// Something else pasted: it asks again, and then the end of input.
	stdin = strings.NewReader("hello\n")
	if code, _, errOut := run("login", "--no-browser"); code != ExitAuth || !strings.Contains(errOut, "it starts with pcode_") {
		t.Fatalf("not a code: exit %d, %q", code, errOut)
	}
}

func TestHaveBrowser(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"BROWSER": "firefox", "SSH_CONNECTION": "1.2.3.4 5 6.7.8.9 22"}, true},
		{map[string]string{"SSH_CONNECTION": "1.2.3.4 5 6.7.8.9 22", "DISPLAY": ":0"}, false},
		{map[string]string{"SSH_TTY": "/dev/ttys001", "DISPLAY": ":0"}, false},
		{map[string]string{"DISPLAY": ":0"}, true},
		{map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
	} {
		withEnv(t, c.env)
		if got := haveBrowser(); got != c.want {
			t.Errorf("%v: %v", c.env, got)
		}
	}
}

func TestLoginRefusals(t *testing.T) {
	ls := newLoginServer(t)
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL})
	aBrowser(t)
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })

	// A code from another sign-in (another challenge): this terminal's
	// verifier doesn't match it.
	openBrowser = func(page string) error {
		u, _ := url.Parse(page)
		q := u.Query()
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"state": {q.Get("state")}, "code": {ls.grant(strings.Repeat("A", 43), q.Get("redirect_uri"))}}.Encode()
		go ls.follow(back.String())
		return nil
	}
	if code, _, errOut := run("login"); code != ExitAuth || !strings.Contains(errOut, "login bad") {
		t.Fatalf("another's code: exit %d, %q", code, errOut)
	}
	ls.wantLanded(t, "failed")

	// A redirect that doesn't bring this sign-in's state back is turned away,
	// and the right one still gets through.
	openBrowser = func(page string) error {
		u, _ := url.Parse(page)
		q := u.Query()
		back, _ := url.Parse(q.Get("redirect_uri"))
		go func() {
			wrong := *back
			wrong.RawQuery = url.Values{"state": {"not-it"}, "code": {"pcode_x"}}.Encode()
			if resp, err := http.Get(wrong.String()); err == nil {
				if resp.StatusCode != 400 {
					t.Errorf("a wrong state was answered %d", resp.StatusCode)
				}
				resp.Body.Close()
			}
			back.RawQuery = url.Values{"state": {q.Get("state")}, "code": {ls.grant(q.Get("challenge"), q.Get("redirect_uri"))}}.Encode()
			ls.follow(back.String())
		}()
		return nil
	}
	if code, _, errOut := run("login"); code != ExitOK {
		t.Fatalf("after a wrong state: exit %d, %q", code, errOut)
	}
	ls.wantLanded(t, "ok")
}

func TestTokenSources(t *testing.T) {
	ls := newLoginServer(t)
	cfg := t.TempDir()
	st := &auth.Store{Path: filepath.Join(cfg, "pony", "credentials.json")}
	if err := st.Put(ls.srv.URL, auth.Saved{Token: "pony_ffffffffffffffffffffffffffffffffffffffff", ID: "tok_cccccccccccc"}); err != nil {
		t.Fatal(err)
	}
	// ONEPONY_TOKEN first: scripts decide.
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg, "ONEPONY_TOKEN": testToken})
	if code, _, errOut := run("whoami"); code != ExitOK {
		t.Fatalf("ONEPONY_TOKEN over the saved one: exit %d, %q", code, errOut)
	}
	// Else the saved one, for this server only.
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg})
	if code, _, errOut := run("whoami"); code != ExitAuth || !strings.Contains(errOut, "pony login") {
		t.Fatalf("a saved token that no longer works: exit %d, %q", code, errOut)
	}
	withEnv(t, map[string]string{"ONEPONY_SERVER": "http://localhost:1", "XDG_CONFIG_HOME": cfg})
	if code, _, errOut := run("whoami"); code != ExitAuth || !strings.Contains(errOut, "not signed in. Run 'pony login'") {
		t.Fatalf("another server: exit %d, %q", code, errOut)
	}
	// A logout whose revoke can't get through keeps the token, to try again.
	withEnv(t, map[string]string{"ONEPONY_SERVER": ls.srv.URL, "XDG_CONFIG_HOME": cfg})
	ls.srv.Close()
	if code, _, errOut := run("logout"); code != ExitTimeout || !strings.Contains(errOut, "kept here") {
		t.Fatalf("logout offline: exit %d, %q", code, errOut)
	}
	if _, ok, _ := st.Get(ls.srv.URL); !ok {
		t.Fatalf("an unrevoked token was forgotten")
	}
}

func TestPKCE(t *testing.T) {
	// S256 as another implementation computes it:
	//   printf %s <verifier> | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '='
	if got := auth.Challenge("dBjftJeZ4CVP-mJ92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "ngF5GsXcbwljx6u133FFr3Xht9xooA_DuaX_3QwODtc" {
		t.Fatalf("challenge %q", got)
	}
	p := auth.NewPKCE()
	if len(p.Verifier) != 43 || p.Challenge != auth.Challenge(p.Verifier) {
		t.Fatalf("%+v", p)
	}
}
