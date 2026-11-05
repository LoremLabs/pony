package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrefs(t *testing.T) {
	dir := t.TempDir()
	p, err := DefaultPrefs(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Get("s1", "a@b.test"); got.Pony != "" {
		t.Fatalf("nothing yet: %+v", got)
	}
	last := Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}}
	if err := p.Put("s1", "a@b.test", last); err != nil {
		t.Fatal(err)
	}
	if got := p.Get("s1", "a@b.test"); got.Pony != "pepper" || got.Chats["pepper"] != "wch_a" {
		t.Fatalf("%+v", got)
	}
	// Someone else, or the same person elsewhere, starts from nothing.
	if got := p.Get("s1", "other@b.test"); got.Pony != "" {
		t.Fatalf("another account saw it: %+v", got)
	}
	if got := p.Get("s2", "a@b.test"); got.Pony != "" {
		t.Fatalf("another server saw it: %+v", got)
	}
	if filepath.Base(p.Path) != "prefs.json" || filepath.Dir(p.Path) != filepath.Join(dir, "pony") {
		t.Fatalf("beside the credentials, please: %s", p.Path)
	}
	if st, err := os.Stat(p.Path); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	b, _ := os.ReadFile(p.Path)
	if strings.Contains(string(b), "pony_") {
		t.Fatalf("no token belongs here: %s", b)
	}
}

func TestPrefsThatCannotBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Prefs{Path: path}
	if got := p.Get("s", "a"); got.Pony != "" {
		t.Fatalf("%+v", got)
	}
	if err := p.Put("s", "a", Last{Pony: "pepper"}); err != nil {
		t.Fatal(err)
	}
	if got := p.Get("s", "a"); got.Pony != "pepper" {
		t.Fatalf("a broken file should mend itself: %+v", got)
	}
}
