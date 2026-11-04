// Package api is pony's client for the OnePony command line API
// (/api/cli/v1/). It speaks HTTPS and JSON, signs each request with the
// account's token, and turns every answer into a value or an *Error.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// DefaultServer is where pony talks unless told otherwise.
const DefaultServer = "https://www.onepony.com"

// maxBody bounds what pony reads of any answer.
const maxBody = 4 << 20

var tokenRE = regexp.MustCompile(`^pony_[0-9a-f]{40}$`)

// ValidToken reports whether s has the shape of a pony token. It says
// nothing about whether the token still works; only the server knows that.
func ValidToken(s string) bool { return tokenRE.MatchString(s) }

// Client calls one OnePony server as one account.
type Client struct {
	Server    string // e.g. https://www.onepony.com, no trailing slash
	Token     string // pony_…; sent only to Server, never printed
	UserAgent string
	HTTP      *http.Client
}

// New returns a client for server (DefaultServer when empty) and token.
func New(server, token, version string) (*Client, error) {
	s, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	return &Client{
		Server:    s,
		Token:     token,
		UserAgent: fmt.Sprintf("pony/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NormalizeServer checks a server URL: https, or plain http only for this
// machine (a token must never cross a network in the clear).
func NormalizeServer(s string) (string, error) {
	if s == "" {
		return DefaultServer, nil
	}
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("server %q: want a URL like https://www.onepony.com", s)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return "", fmt.Errorf("server %q: plain http is only for localhost; use https", s)
		}
	default:
		return "", fmt.Errorf("server %q: want a URL like https://www.onepony.com", s)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Error is an answer the server gave instead of a result.
type Error struct {
	Status  int    // the HTTP status
	Code    string // stable: not_signed_in, not_found, …
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return strings.ReplaceAll(e.Code, "_", " ")
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// NetError is a request that never got an answer: no connection, a
// timeout, or an answer that wasn't the API's.
type NetError struct {
	Err     error
	Timeout bool
}

func (e *NetError) Error() string { return e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// Get calls GET path (e.g. /api/cli/v1/me) and decodes the answer's body
// into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ne net.Error
		return &NetError{Err: errors.New(scrub(err.Error(), c.Token)), Timeout: errors.As(err, &ne) && ne.Timeout()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return &NetError{Err: fmt.Errorf("reading the answer: %w", err)}
	}

	var env struct {
		OK    *bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.OK == nil {
		// Not the CLI API's shape: a proxy's page, an old server, the
		// wrong host.
		return &NetError{Err: fmt.Errorf("%s answered HTTP %d, not the OnePony API (is --server right?)", c.Server, resp.StatusCode)}
	}
	if !*env.OK || resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		if env.Error != nil {
			e.Code, e.Message = env.Error.Code, env.Error.Message
		}
		return e
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return &NetError{Err: fmt.Errorf("an answer pony can't read: %w", err)}
		}
	}
	return nil
}

// scrub keeps the token out of an error that might quote the request.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "pony_…")
}

// Me is GET /api/cli/v1/me: who the token is.
type Me struct {
	Address string    `json:"address"`
	Name    string    `json:"name,omitempty"`
	Token   TokenInfo `json:"token"`
}

// TokenInfo describes the token in use. Ponies is "all" or a list of
// slugs.
type TokenInfo struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Ponies  json.RawMessage `json:"ponies"`
	Expires string          `json:"expires"`
}

// Reach says what the token reaches: nil for every pony, else the slugs.
func (t TokenInfo) Reach() []string {
	var all string
	if json.Unmarshal(t.Ponies, &all) == nil && all == "all" {
		return nil
	}
	var slugs []string
	_ = json.Unmarshal(t.Ponies, &slugs)
	return slugs
}

// Pony is one entry of GET /api/cli/v1/ponies.
type Pony struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Status  string `json:"status"` // working, needs_you, waiting, available
	Doing   string `json:"doing,omitempty"`
	Ready   bool   `json:"ready"` // false while it gets ready to start
}

// Me asks who the token is.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	if err := c.Get(ctx, "/api/cli/v1/me", &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Ponies lists the ponies the token reaches.
func (c *Client) Ponies(ctx context.Context) ([]Pony, error) {
	var r struct {
		Ponies []Pony `json:"ponies"`
	}
	if err := c.Get(ctx, "/api/cli/v1/ponies", &r); err != nil {
		return nil, err
	}
	if r.Ponies == nil {
		r.Ponies = []Pony{}
	}
	return r.Ponies, nil
}
