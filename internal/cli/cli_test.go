package cli

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

var testBuild = Build{Version: "1.2.3", Commit: "0123456789abcdef", BuildTime: "2026-10-30T00:00:00Z", InstallMethod: "release"}

func run(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = Run(args, testBuild, &o, &e)
	return code, o.String(), e.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run("version")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out, "pony 1.2.3 (") || !strings.Contains(out, "commit 0123456789ab,") {
		t.Fatalf("unexpected output %q", out)
	}
	if code, out2, _ := run("--version"); code != ExitOK || out2 != out {
		t.Fatalf("--version: exit %d, %q", code, out2)
	}
}

func TestVersionJSON(t *testing.T) {
	code, out, _ := run("version", "--json")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	var v versionInfo
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v: %q", err, out)
	}
	if v.Version != "1.2.3" || v.Commit != testBuild.Commit || v.OS == "" || v.Arch == "" {
		t.Fatalf("unexpected %+v", v)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		code, out, _ := run(args...)
		if code != ExitOK || !strings.Contains(out, "Usage:") || !strings.Contains(out, "version") {
			t.Fatalf("%v: exit %d, %q", args, code, out)
		}
	}
}

func TestHelpForCommand(t *testing.T) {
	code, _, errOut := run("help", "version")
	if code != ExitOK || !strings.Contains(errOut, "pony version [--json]") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{{"nope"}, {"help", "nope"}, {"version", "--bogus"}, {"version", "extra"}}
	for _, args := range cases {
		if code, _, _ := run(args...); code != ExitUsage {
			t.Fatalf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestInstalledBy(t *testing.T) {
	cases := []struct{ method, exe, want string }{
		{"source", "/opt/homebrew/Cellar/pony/1.2.3/bin/pony", "built from source"},
		{"release", "/opt/homebrew/Cellar/pony/1.2.3/bin/pony", "homebrew"},
		{"release", "/home/linuxbrew/.linuxbrew/Cellar/pony/1.2.3/bin/pony", "homebrew"},
		{"release", "/Users/sam/.local/bin/pony", "release"},
	}
	for _, c := range cases {
		if got := installedBy(c.method, c.exe); got != c.want {
			t.Errorf("installedBy(%q, %q) = %q, want %q", c.method, c.exe, got, c.want)
		}
	}
}

func TestHelpHasThePonyOnATerminal(t *testing.T) {
	aTerminal(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows gets the words alone")
	}
	withEnv(t, aPonyTerminal)
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		code, out, _ := run(args...)
		i := strings.Index(out, "pony talks to your OnePony ponies")
		if code != ExitOK || i < 0 {
			t.Fatalf("%v: exit %d, %q", args, code, out)
		}
		top := out[:i]
		if !strings.Contains(top, "\x1b[48;5;") || strings.Count(top, "\n") > ponyRows+1 {
			t.Fatalf("%v: no pony over the help: %q", args, top)
		}
		if !strings.HasSuffix(top, "\x1b[0m\n\n") {
			t.Fatalf("%v: the pony should end clean, a line above the words: %q", args, top[max(0, len(top)-20):])
		}
	}
}

func TestHelpIsItsWordsAloneElsewhere(t *testing.T) {
	for _, env := range []map[string]string{aPonyTerminal, {"TERM": "xterm-256color", "NO_COLOR": "1"}, {"TERM": "xterm-256color", "CI": "true"}, {"TERM": "dumb"}} {
		withEnv(t, env)
		// Not a terminal at all.
		if code, out, _ := run("help"); code != ExitOK || !strings.HasPrefix(out, "pony talks") {
			t.Fatalf("%v: exit %d, %q", env, code, out)
		}
	}
	aTerminal(t)
	for _, env := range []map[string]string{{"TERM": "xterm-256color", "NO_COLOR": "1"}, {"TERM": "xterm-256color", "CI": "true"}, {"TERM": "dumb"}, {"TERM": "xterm"}} {
		withEnv(t, env)
		if code, out, _ := run("help"); code != ExitOK || !strings.HasPrefix(out, "pony talks") {
			t.Fatalf("on a terminal, %v: exit %d, %q", env, code, out[:min(40, len(out))])
		}
	}
}

// A command's own help stays short.
func TestCommandHelpHasNoPony(t *testing.T) {
	aTerminal(t)
	withEnv(t, aPonyTerminal)
	if _, out, errOut := run("help", "log"); strings.Contains(out+errOut, "\x1b[48;5;") {
		t.Fatalf("%q", out+errOut)
	}
}
