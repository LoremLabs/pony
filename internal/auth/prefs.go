package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Prefs is the small file that remembers where a conversation was: which pony
// you last talked to and which chat with each. No token goes in it. It's kept
// per server and per account, so signing in as someone else never opens on
// what the last account was looking at. It's a convenience: if it can't be
// read or written, pony just asks again.
type Prefs struct {
	Path string
}

// Last is where one account was.
type Last struct {
	Pony  string            `json:"pony,omitempty"`
	Chats map[string]string `json:"chats,omitempty"` // pony → its chat's card
}

type prefsFile map[string]map[string]Last

// DefaultPrefs sits beside the credentials file.
func DefaultPrefs(getenv func(string) string) (*Prefs, error) {
	s, err := DefaultStore(getenv)
	if err != nil {
		return nil, err
	}
	return &Prefs{Path: filepath.Join(filepath.Dir(s.Path), "prefs.json")}, nil
}

func (p *Prefs) read() prefsFile {
	f := prefsFile{}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil || f == nil {
		return prefsFile{}
	}
	return f
}

// Get is where this account on this server was, if anywhere.
func (p *Prefs) Get(server, address string) Last {
	return p.read()[server][address]
}

// Put remembers where this account on this server is.
func (p *Prefs) Put(server, address string, last Last) error {
	f := p.read()
	if f[server] == nil {
		f[server] = map[string]Last{}
	}
	f[server][address] = last
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".prefs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.Path)
}
