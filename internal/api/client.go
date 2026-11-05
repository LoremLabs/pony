// Package api is pony's client for the OnePony command line API
// (/api/cli/v1/). It speaks HTTPS and JSON, signs each request with the
// account's token, and turns every answer into a value or an *Error.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
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
	Status  int // the HTTP status of an answer that wasn't the API's; 0 for none
}

func (e *NetError) Error() string { return e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// Get calls GET path (e.g. /api/cli/v1/me) and decodes the answer's body
// into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, nil, out)
}

// do sends one request and decodes the API's answer into out. Every answer
// of the API is {ok: true, …} or {ok: false, error: {code, message}}; an
// answer of any other shape is a NetError (a proxy's page, a server that is
// starting, the wrong host).
func (c *Client) do(ctx context.Context, method, path string, body []byte, header http.Header, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, rd)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ne net.Error
		return &NetError{Err: errors.New(scrub(err.Error(), c.Token)), Timeout: errors.As(err, &ne) && ne.Timeout()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return &NetError{Err: fmt.Errorf("reading the answer: %w", err)}
	}

	var env struct {
		OK    *bool           `json:"ok"`
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(data, &env) != nil || env.OK == nil {
		return &NetError{Err: fmt.Errorf("%s answered HTTP %d, not the OnePony API (is --server right?)", c.Server, resp.StatusCode), Status: resp.StatusCode}
	}
	if !*env.OK || resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode, Code: env.Code}
		var obj struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		var text string
		if json.Unmarshal(env.Error, &obj) == nil {
			if obj.Code != "" {
				e.Code = obj.Code
			}
			e.Message = obj.Message
		} else if json.Unmarshal(env.Error, &text) == nil {
			e.Message = text
		}
		return e
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
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

// TokenInfo describes the token in use. Ponies is "*" (every pony) or a
// list of slugs, kept as the server said it: nothing here reads it.
type TokenInfo struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Ponies  json.RawMessage `json:"ponies"`
	Expires string          `json:"expires"`
}

// Pony is one entry of GET /api/cli/v1/ponies.
type Pony struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Status  string `json:"status"` // working, needs_you, waiting, available
	Doing   string `json:"doing,omitempty"`
	Ready   bool   `json:"ready"`          // false while it gets ready to start
	Card    string `json:"card,omitempty"` // the card it works on, or waits on you on
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

// Pony asks for one pony the token reaches.
func (c *Client) Pony(ctx context.Context, slug string) (*Pony, error) {
	var r struct {
		Pony Pony `json:"pony"`
	}
	if err := c.Get(ctx, "/api/cli/v1/ponies/"+url.PathEscape(slug), &r); err != nil {
		return nil, err
	}
	return &r.Pony, nil
}

// Said is the answer to a line sent to a pony: the card it went on and its
// place there (the cursor to follow the card from).
type Said struct {
	Card string `json:"card"`
	Seq  int    `json:"seq"`
	At   string `json:"at,omitempty"`
}

// NewCard names a fresh chat for a line: OnePony makes the card under this
// id when the line arrives, so the chat has its address before its first
// line (the web page does the same).
func NewCard() string { return "wch_" + randomHex(12) }

// NewKey makes an idempotency key: a line sent again under the same key is
// the first one, never a second line.
func NewKey() string { return "pony-" + randomHex(16) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on the systems Go supports
	}
	return hex.EncodeToString(b)
}

// Retry is how often a request that got no answer is sent again, and how
// long to wait before each.
var Retry = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Say sends a line to a pony, on card (a chat that exists, or one NewCard
// named), and returns where it went. A request that got no answer — a
// network failure, a timeout, a server that answered something else, or
// one that kept the line but couldn't wake the pony (502, 503, 504) — is
// sent again under the same key, so the pony gets the line once.
func (c *Client) Say(ctx context.Context, slug, text, card, key string) (*Said, error) {
	body, err := json.Marshal(map[string]string{"text": text, "card": card})
	if err != nil {
		return nil, err
	}
	h := http.Header{"Idempotency-Key": []string{key}}
	for attempt := 0; ; attempt++ {
		var s Said
		err = c.do(ctx, http.MethodPost, "/api/cli/v1/ponies/"+url.PathEscape(slug)+"/chat", body, h, &s)
		if err == nil && s.Card != "" {
			return &s, nil
		}
		if err == nil {
			err = &NetError{Err: errors.New("the answer named no card")}
		}
		if attempt >= len(Retry) || !retryable(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(Retry[attempt]):
		}
	}
}

func retryable(err error) bool {
	var ne *NetError
	if errors.As(err, &ne) {
		return true
	}
	var ae *Error
	return errors.As(err, &ae) && (ae.Status == 502 || ae.Status == 503 || ae.Status == 504)
}

// Card is a card's state and what was said on it after a cursor.
type Card struct {
	Card struct {
		ID    string `json:"id"`
		Pony  string `json:"pony"`
		State string `json:"state"` // working, queued, needs_you, waiting, failed, idle
	} `json:"card"`
	Lines []Line `json:"lines"`
	Next  int    `json:"next"`
}

// Line is one thing said on a card.
type Line struct {
	Seq  int    `json:"seq"`
	From string `json:"from"` // you | pony
	Text string `json:"text"`
	At   string `json:"at,omitempty"`
	Via  string `json:"via,omitempty"` // web | mail | comment
}

// Busy reports whether the pony is still at it: a run on the card, or a
// wake for it waiting its turn.
func Busy(state string) bool { return state == "working" || state == "queued" }

// Event is one line of a log: something a pony did, or something done on its
// cards, in the feed's words.
type Event struct {
	ID    string `json:"id"` // a line's own, for telling a line seen from one that isn't
	At    string `json:"at"`
	Pony  string `json:"pony"`
	Who   string `json:"who"` // you, an address, or the pony
	Kind  string `json:"kind"`
	Card  string `json:"card,omitempty"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text"`          // what happened, after who
	Run   string `json:"run,omitempty"` // the run it belongs to, when it says
	RID   string `json:"rid,omitempty"` // the request that held that run
}

// Log is GET /api/cli/v1/log: lines, oldest first, and the cursor to read on
// from. Lines near the cursor come again: a follower drops what it has seen.
type Log struct {
	Events []Event `json:"events"`
	Next   string  `json:"next"`
}

// ReadLog reads the newest limit lines of one pony's log, or of every pony
// the token reaches when slug is empty, from the cursor after if there is one.
func (c *Client) ReadLog(ctx context.Context, slug, after string, limit int) (*Log, error) {
	path := "/api/cli/v1/log"
	if slug != "" {
		path = "/api/cli/v1/ponies/" + url.PathEscape(slug) + "/log"
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	var r Log
	if err := c.Get(ctx, path+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	if r.Next == "" {
		r.Next = after
	}
	return &r, nil
}

// ReadCard reads a card, and the lines after the cursor after (0: from
// the start).
func (c *Client) ReadCard(ctx context.Context, card string, after int) (*Card, error) {
	path := "/api/cli/v1/cards/" + url.PathEscape(card)
	if after > 0 {
		path += "?after=" + strconv.Itoa(after)
	}
	var r Card
	if err := c.Get(ctx, path, &r); err != nil {
		return nil, err
	}
	if r.Next < after {
		r.Next = after
	}
	return &r, nil
}

// Granted is what a sign-in's exchange answers: the token, and what it is.
type Granted struct {
	Secret string    `json:"secret"`
	Token  TokenInfo `json:"token"`
}

// Exchange swaps the one-time code the authorize page gave the browser,
// and the PKCE verifier only this terminal holds, for a token (POST
// /api/cli/token). redirect is the one the sign-in named ("" when the code
// was typed). A code is good for one try.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirect string) (*Granted, error) {
	body, err := json.Marshal(map[string]string{"code": code, "verifier": verifier, "redirect": redirect})
	if err != nil {
		return nil, err
	}
	var g Granted
	if err := c.do(ctx, http.MethodPost, "/api/cli/token", body, nil, &g); err != nil {
		return nil, err
	}
	if !ValidToken(g.Secret) {
		return nil, &NetError{Err: errors.New("the sign-in answered no token")}
	}
	return &g, nil
}

// Logout revokes the token the client signs in with (POST
// /api/cli/v1/logout): it stops working at once, as Revoke on the Account
// page does. It returns the revoked token's id.
func (c *Client) Logout(ctx context.Context) (string, error) {
	var r struct {
		Revoked string `json:"revoked"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/cli/v1/logout", []byte("{}"), nil, &r); err != nil {
		return "", err
	}
	return r.Revoked, nil
}
