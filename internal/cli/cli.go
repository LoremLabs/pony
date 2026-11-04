// Package cli is the pony command line: argument parsing, dispatch and the
// exit codes scripts can rely on.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Exit codes. They are part of the contract with scripts: never renumber one.
const (
	ExitOK       = 0
	ExitError    = 1 // anything not covered below
	ExitUsage    = 2 // bad command, flag or argument
	ExitAuth     = 3 // not signed in, or not allowed
	ExitNotFound = 4 // no such pony or card
	ExitWaiting  = 5 // the pony is waiting on you (a hold, a question)
	ExitTimeout  = 6 // timed out, or the network failed
	ExitFailed   = 7 // the pony's work failed
)

// Build describes this binary; main stamps it at release time.
type Build struct {
	Version       string
	Commit        string
	BuildTime     string
	InstallMethod string
}

type command struct {
	name    string
	summary string
	run     func(args []string, b Build, stdout, stderr io.Writer) int
}

// commands lists only what this version can do. A command appears here when
// it works, never before.
var commands []command

func init() {
	commands = []command{
		{"login", "Sign in, in your browser", runLogin},
		{"logout", "Sign out: revoke and forget the token login saved", runLogout},
		{"whoami", "Show the account your token signs in as", runWhoami},
		{"list", "List your ponies and what each is doing", runList},
		{"ask", "Say something to a pony and wait for its answer", runAsk},
		{"watch", "Follow a card until its pony is done with it", runWatch},
		{"status", "Show what one pony is doing", runStatus},
		{"version", "Print the version of pony", runVersion},
		{"help", "Show help for pony or one of its commands", runHelp},
	}
}

// Run executes the command line and returns the process exit code.
func Run(args []string, b Build, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stdout)
		return ExitOK
	}
	switch args[0] {
	case "-h", "--help":
		usage(stdout)
		return ExitOK
	case "-v", "--version":
		return runVersion(nil, b, stdout, stderr)
	}
	if c := find(args[0]); c != nil {
		return c.run(args[1:], b, stdout, stderr)
	}
	fmt.Fprintf(stderr, "pony: unknown command %q\nRun 'pony help' to see the commands.\n", args[0])
	return ExitUsage
}

func find(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `pony talks to your OnePony ponies from the terminal.

Usage:
  pony <command> [flags]

Commands:
`)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, `
Sign in with 'pony login'. Scripts can set ONEPONY_TOKEN to a token from
your Account page (https://www.onepony.com/settings/account) instead.

Run 'pony help <command>' for more about a command.
`)
}

func runHelp(args []string, b Build, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stdout)
		return ExitOK
	}
	c := find(args[0])
	if c == nil {
		fmt.Fprintf(stderr, "pony: unknown command %q\n", args[0])
		return ExitUsage
	}
	return c.run([]string{"--help"}, b, stdout, stderr)
}

// versionInfo is the --json shape of `pony version`.
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time,omitempty"`
	Installed string `json:"installed"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func runVersion(args []string, b Build, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the version as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: pony version [--json]\n\nPrint the version of pony. Works offline.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return ExitOK
		}
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return ExitUsage
	}
	v := versionInfo{
		Version:   b.Version,
		Commit:    b.Commit,
		BuildTime: b.BuildTime,
		Installed: installedBy(b.InstallMethod, executable()),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(v); err != nil {
			fmt.Fprintf(stderr, "pony: %v\n", err)
			return ExitError
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "pony %s (%s/%s, %s", v.Version, v.OS, v.Arch, v.Installed)
	if v.Commit != "" && v.Commit != "dev" {
		short := v.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		fmt.Fprintf(stdout, ", commit %s", short)
	}
	if v.BuildTime != "" {
		fmt.Fprintf(stdout, ", built %s", v.BuildTime)
	}
	fmt.Fprintln(stdout, ")")
	return ExitOK
}

func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// installedBy says how this copy got here: a release binary inside a
// Homebrew Cellar is Homebrew's to upgrade; any other release binary came
// from the install script or a download; anything else was built locally.
func installedBy(method, exe string) string {
	if method != "release" {
		return "built from source"
	}
	if strings.Contains(filepath.ToSlash(exe), "/Cellar/") {
		return "homebrew"
	}
	return "release"
}
