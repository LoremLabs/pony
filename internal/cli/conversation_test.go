package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/tui"
)

// atAKeyboard is a person at a terminal, with the conversation stood in for.
func atAKeyboard(t *testing.T, res tui.Result) *[]tui.Options {
	t.Helper()
	aTerminal(t)
	stdinIsTerminal = func() bool { return true }
	old := conversation
	var opened []tui.Options
	conversation = func(_ context.Context, o tui.Options) tui.Result {
		opened = append(opened, o)
		return res
	}
	t.Cleanup(func() { conversation = old })
	return &opened
}

func conversationServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/cli/v1/me":
			w.Write([]byte(`{"ok":true,"address":"ada@example.com"}`))
		case r.URL.Path == "/api/cli/v1/ponies/pepper":
			w.Write([]byte(`{"ok":true,"pony":{"slug":"pepper","ready":true}}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"ok":false,"error":{"code":"no_such_pony","message":"no such pony"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBarePonyOpensAConversationAtAKeyboard(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{Pony: "pepper"})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	code, out, errOut := run()
	if code != ExitOK || len(*opened) != 1 {
		t.Fatalf("exit %d, opened %d: %q %q", code, len(*opened), out, errOut)
	}
	o := (*opened)[0]
	if o.Pony != "" || o.Address != "ada@example.com" || o.Client == nil || o.Plain {
		t.Fatalf("%+v", o)
	}
	if out != "Left pepper; it carries on.\n" {
		t.Fatalf("%q", out)
	}
}

func TestLeavingMidRunSaysHowToFollow(t *testing.T) {
	atAKeyboard(t, tui.Result{Pony: "pepper", Card: "wch_a", Busy: true})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	if _, out, _ := run(); out != "Left pepper; it carries on. To follow it: pony watch wch_a\n" {
		t.Fatalf("%q", out)
	}
}

func TestPonyByName(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	if code, _, errOut := run("pepper"); code != ExitOK || len(*opened) != 1 || (*opened)[0].Pony != "pepper" {
		t.Fatalf("exit %d: %q %+v", code, errOut, *opened)
	}
}

// A typo doesn't open a screen.
func TestNoSuchPonyIsAnUnknownCommand(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	code, _, errOut := run("lst")
	if code != ExitUsage || len(*opened) != 0 || !strings.Contains(errOut, `unknown command "lst"`) {
		t.Fatalf("exit %d, opened %d: %q", code, len(*opened), errOut)
	}
}

// A command always wins, and keeps its own meaning.
func TestCommandsComeBeforePonies(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	for _, args := range [][]string{{"version"}, {"help"}, {"--help"}, {"-h"}} {
		if code, _, _ := run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("opened a conversation: %+v", *opened)
	}
}

func TestNotSignedInSaysSoOnTheTerminal(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{})
	withEnv(t, map[string]string{"ONEPONY_SERVER": "http://localhost:1"})
	code, _, errOut := run()
	if code != ExitAuth || len(*opened) != 0 || !strings.Contains(errOut, "pony login") {
		t.Fatalf("exit %d, opened %d: %q", code, len(*opened), errOut)
	}
}

func TestNoConversationForAProgram(t *testing.T) {
	opened := atAKeyboard(t, tui.Result{})
	s := conversationServer(t)
	cases := []struct {
		name  string
		env   map[string]string
		input bool
		tty   bool
	}{
		{"stdin not a terminal", nil, false, true},
		{"stdout not a terminal", nil, true, false},
		{"a dumb terminal", map[string]string{"TERM": "dumb"}, true, true},
		{"CI", map[string]string{"CI": "true"}, true, true},
	}
	for _, c := range cases {
		env := map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL}
		for k, v := range c.env {
			env[k] = v
		}
		withEnv(t, env)
		stdinIsTerminal = func() bool { return c.input }
		tty := c.tty
		isTerminal = func(io.Writer) bool { return tty }
		code, out, _ := run()
		if code != ExitOK || !strings.Contains(out, "Usage:") {
			t.Fatalf("%s: exit %d, %q", c.name, code, out)
		}
		if code, _, errOut := run("pepper"); code != ExitUsage || !strings.Contains(errOut, "unknown command") {
			t.Fatalf("%s: a pony by name: exit %d, %q", c.name, code, errOut)
		}
		if len(*opened) != 0 {
			t.Fatalf("%s: opened a conversation", c.name)
		}
	}
}

// Off a terminal the screen's check is honest: /dev/null isn't one, though
// it's a character device.
func TestDevNullIsNoTerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null read as a terminal")
	}
}

func TestConversationEndedByTheServer(t *testing.T) {
	atAKeyboard(t, tui.Result{Err: &api.Error{Status: 401, Code: "not_signed_in", Message: "not signed in"}})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	if code, _, errOut := run(); code != ExitAuth || !strings.Contains(errOut, "pony login") {
		t.Fatalf("exit %d: %q", code, errOut)
	}
}

func TestConversationEndedByAnythingElse(t *testing.T) {
	atAKeyboard(t, tui.Result{Err: errors.New("you have no ponies yet")})
	s := conversationServer(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.URL})
	if code, _, errOut := run(); code != ExitError || !strings.Contains(errOut, "no ponies") {
		t.Fatalf("exit %d: %q", code, errOut)
	}
}
