package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "pony_0123456789abcdef0123456789abcdef01234567"

// fakeServer answers the CLI API as OnePony does, for testToken only.
func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	answer := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "pony/1.2.3 (") {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			answer(w, 401, `{"ok":false,"error":{"code":"not_signed_in","message":"not signed in"}}`)
			return
		}
		switch r.URL.Path {
		case "/api/cli/v1/me":
			answer(w, 200, `{"ok":true,"address":"ada@example.com","name":"Ada","token":{"id":"tok_aaaaaaaaaaaa","name":"Laptop","ponies":["pepper"],"expires":"2026-12-01T20:00:00Z"}}`)
		case "/api/cli/v1/ponies":
			answer(w, 200, `{"ok":true,"ponies":[{"slug":"front-desk","name":"Front Desk","address":"front-desk@mail.example","status":"working","doing":"Taxes","ready":true},{"slug":"pepper","name":"Pepper","address":"pepper@mail.example","status":"available","doing":"","ready":false}]}`)
		default:
			answer(w, 404, `{"ok":false,"error":{"code":"not_found","message":"no such route"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withEnv gives a test its environment, with a config dir of its own: no
// test reads or writes the credentials of whoever runs it.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	if _, ok := env["XDG_CONFIG_HOME"]; !ok {
		env["XDG_CONFIG_HOME"] = t.TempDir()
	}
	old := getenv
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { getenv = old })
}

func noToken(t *testing.T, out, errOut string) {
	t.Helper()
	if strings.Contains(out, testToken) || strings.Contains(errOut, testToken) {
		t.Fatalf("the token was printed: %q %q", out, errOut)
	}
}

func TestWhoami(t *testing.T) {
	srv := fakeServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": srv.URL})
	code, out, errOut := run("whoami")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := "Ada <ada@example.com>\ntoken \"Laptop\" reaches pepper, until 2026-12-01\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if !strings.Contains(errOut, "using "+srv.URL) {
		t.Fatalf("a server other than the default should be named on stderr: %q", errOut)
	}
	noToken(t, out, errOut)

	code, out, _ = run("whoami", "--json")
	var me map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &me) != nil || me["address"] != "ada@example.com" {
		t.Fatalf("--json: exit %d, %q", code, out)
	}
}

func TestList(t *testing.T) {
	srv := fakeServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken})
	code, out, errOut := run("list", "--server", srv.URL)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "PONY") ||
		!strings.Contains(lines[1], "front-desk") || !strings.Contains(lines[1], "working") || !strings.HasSuffix(lines[1], "Taxes") ||
		!strings.Contains(lines[2], "getting ready") {
		t.Fatalf("unexpected table:\n%s", out)
	}

	code, out, _ = run("list", "--json", "--server", srv.URL)
	var ponies []map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &ponies) != nil || len(ponies) != 2 || ponies[1]["ready"] != false {
		t.Fatalf("--json: exit %d, %q", code, out)
	}
}

func TestSignedOut(t *testing.T) {
	srv := fakeServer(t)
	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{"no token", "", "not signed in. Run 'pony login'"},
		{"not a token", "hunter2", "isn't a pony token"},
		{"revoked", "pony_ffffffffffffffffffffffffffffffffffffffff", "revoked, has expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{"ONEPONY_TOKEN": tc.token, "ONEPONY_SERVER": srv.URL})
			code, out, errOut := run("list")
			if code != ExitAuth || out != "" || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			if tc.token != "" && strings.Contains(errOut, tc.token) {
				t.Fatalf("the token was printed: %q", errOut)
			}
		})
	}
}

func TestFailures(t *testing.T) {
	srv := fakeServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken})

	// A server that isn't the API: the network's fault, not the account's.
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>hi</html>")) }))
	defer html.Close()
	if code, _, errOut := run("whoami", "--server", html.URL); code != ExitTimeout || !strings.Contains(errOut, "not the OnePony API") {
		t.Fatalf("html: exit %d, %q", code, errOut)
	}

	// Nobody listening.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if code, _, errOut := run("whoami", "--server", url); code != ExitTimeout {
		t.Fatalf("dead: exit %d, %q", code, errOut)
	}

	// Plain http off this machine, or not a URL: a usage error, and no
	// request carries the token anywhere.
	for _, s := range []string{"http://example.com", "ftp://x", "https://x/path"} {
		if code, _, errOut := run("whoami", "--server", s); code != ExitUsage {
			t.Fatalf("%s: exit %d, %q", s, code, errOut)
		}
	}

	if code, _, _ := run("list", "extra", "--server", srv.URL); code != ExitUsage {
		t.Fatalf("an argument list doesn't take: exit %d", code)
	}
}

func TestHelpListsWorkingCommands(t *testing.T) {
	_, out, _ := run("help")
	for _, c := range []string{"login", "logout", "whoami", "list", "ask", "watch", "status", "version", "ONEPONY_TOKEN"} {
		if !strings.Contains(out, c) {
			t.Fatalf("help lacks %q:\n%s", c, out)
		}
	}

}
