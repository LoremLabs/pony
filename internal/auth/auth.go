// Package auth is how pony signs in: the PKCE pair `pony login` sends the
// browser with, and the file that keeps the token it gets, one per server.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// PKCE is a sign-in's proof (RFC 7636, S256): the challenge goes through
// the browser; the verifier never leaves this terminal until the exchange.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE makes a verifier of 32 random bytes and its challenge.
func NewPKCE() PKCE {
	v := base64.RawURLEncoding.EncodeToString(random(32))
	return PKCE{Verifier: v, Challenge: Challenge(v)}
}

// Challenge is the S256 challenge of a verifier: base64url, unpadded, of
// its sha256.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewState makes the value a sign-in's redirect must bring back.
func NewState() string { return hex.EncodeToString(random(16)) }

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on the systems Go supports
	}
	return b
}

// Saved is what `pony login` keeps for one server.
type Saved struct {
	Token   string `json:"token"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Expires string `json:"expires,omitempty"`
}

type file struct {
	Servers map[string]Saved `json:"servers"`
}

// Store is the credentials file: readable by its owner only.
type Store struct {
	Path string
}

// DefaultStore is $XDG_CONFIG_HOME/pony/credentials.json, else
// ~/.config/pony/credentials.json (on Windows, the user's AppData).
func DefaultStore(getenv func(string) string) (*Store, error) {
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return &Store{Path: filepath.Join(d, "pony", "credentials.json")}, nil
	}
	if runtime.GOOS == "windows" {
		d, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		return &Store{Path: filepath.Join(d, "pony", "credentials.json")}, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(h, ".config", "pony", "credentials.json")}, nil
}

func (s *Store) read() (file, error) {
	f := file{Servers: map[string]Saved{}}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s isn't pony's credentials file: %w", s.Path, err)
	}
	if f.Servers == nil {
		f.Servers = map[string]Saved{}
	}
	return f, nil
}

// Get returns what is saved for server, if anything.
func (s *Store) Get(server string) (Saved, bool, error) {
	f, err := s.read()
	if err != nil {
		return Saved{}, false, err
	}
	v, ok := f.Servers[server]
	return v, ok && v.Token != "", nil
}

// Put saves v for server, replacing what was there.
func (s *Store) Put(server string, v Saved) error {
	f, err := s.read()
	if err != nil {
		return err
	}
	f.Servers[server] = v
	return s.write(f)
}

// Forget removes what is saved for server; the file goes when nothing is
// left in it.
func (s *Store) Forget(server string) error {
	f, err := s.read()
	if err != nil {
		return err
	}
	delete(f.Servers, server)
	if len(f.Servers) == 0 {
		err := os.Remove(s.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return s.write(f)
}

// Servers lists the servers something is saved for.
func (s *Store) Servers() ([]string, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	var out []string
	for k := range f.Servers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// write replaces the file whole: a new file beside it, mode 0600, renamed
// over it, so a crash leaves the old one or the new one.
func (s *Store) write(f file) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
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
	return os.Rename(tmp.Name(), s.Path)
}
