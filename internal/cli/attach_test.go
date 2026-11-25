package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/loremlabs/pony/internal/api"
)

// attachWith stands in for txco: where it is (or none), whether it takes a
// site, and what pony handed to it.
func attachWith(t *testing.T, path string, takesSite bool) *[]string {
	t.Helper()
	oldLook, oldTakes, oldHand, oldEnv := lookTxco, txcoTakesSite, handTo, getenv
	t.Cleanup(func() { lookTxco, txcoTakesSite, handTo, getenv = oldLook, oldTakes, oldHand, oldEnv })
	lookTxco = func() (string, error) {
		if path == "" {
			return "", exec.ErrNotFound
		}
		return path, nil
	}
	txcoTakesSite = func(string) bool { return takesSite }
	got := &[]string{}
	handTo = func(p string, argv []string, _ io.Writer) int {
		*got = append([]string{p}, argv...)
		return 0
	}
	getenv = func(string) string { return "" }
	return got
}

func TestAttachHandsToTxco(t *testing.T) {
	got := attachWith(t, "/opt/bin/txco", true)
	if code, _, stderr := run("attach", "sc.test"); code != ExitOK {
		t.Fatalf("code %d: %s", code, stderr)
	}
	want := []string{"/opt/bin/txco", "txco", "attach", api.DefaultServer + "?pony=sc.test"}
	if strings.Join(*got, " ") != strings.Join(want, " ") {
		t.Errorf("handed %q, want %q", *got, want)
	}
}

func TestAttachNoPony(t *testing.T) {
	got := attachWith(t, "/opt/bin/txco", true)
	run("attach")
	if len(*got) != 4 || (*got)[3] != api.DefaultServer {
		t.Errorf("handed %q", *got)
	}
}

func TestAttachServer(t *testing.T) {
	got := attachWith(t, "/opt/bin/txco", true)
	getenv = func(k string) string {
		if k == "ONEPONY_SERVER" {
			return "http://localhost:5273"
		}
		return ""
	}
	run("attach", "sc.test")
	if len(*got) != 4 || (*got)[3] != "http://localhost:5273?pony=sc.test" {
		t.Errorf("handed %q", *got)
	}
	run("attach", "--server", "https://www.example.com", "sc.test")
	if (*got)[3] != "https://www.example.com?pony=sc.test" {
		t.Errorf("handed %q", *got)
	}
}

func TestAttachNoTxco(t *testing.T) {
	got := attachWith(t, "", true)
	code, _, stderr := run("attach", "sc.test")
	if code != ExitError || !strings.Contains(stderr, "brew install loremlabs/txco/txco") || len(*got) != 0 {
		t.Errorf("code %d, handed %q: %s", code, *got, stderr)
	}
}

func TestAttachOldTxco(t *testing.T) {
	got := attachWith(t, "/opt/bin/txco", false)
	code, _, stderr := run("attach", "sc.test")
	if code != ExitError || !strings.Contains(stderr, "brew upgrade txco") || len(*got) != 0 {
		t.Errorf("code %d, handed %q: %s", code, *got, stderr)
	}
}

func TestAttachNotAPony(t *testing.T) {
	got := attachWith(t, "/opt/bin/txco", true)
	for _, arg := range []string{"Sc Test", "sc.test?x=1", "../x", "a&pony=b"} {
		if code, _, _ := run("attach", arg); code != ExitUsage {
			t.Errorf("%q: code %d", arg, code)
		}
	}
	if code, _, _ := run("attach", "a", "b"); code != ExitUsage {
		t.Errorf("two ponies: code %d", code)
	}
	if len(*got) != 0 {
		t.Errorf("handed %q", *got)
	}
}

// The probe reads a real program's usage.
func TestTxcoTakesSite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	script := func(name, usage string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+usage+"' >&2\nexit 2\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if !txcoTakesSite(script("new", "usage: txco attach [--address URL] [--yes] [<site> | <code> | list]")) {
		t.Error("a txco that takes a site was refused")
	}
	if txcoTakesSite(script("old", "usage: txco attach [--address URL] [--yes] [<code> | list]")) {
		t.Error("an old txco was taken")
	}
	if txcoTakesSite(filepath.Join(dir, "missing")) {
		t.Error("no program was taken")
	}
}
