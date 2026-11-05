package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInert(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain text", "It is 14:53 in Paris.", "It is 14:53 in Paris."},
		{"an answer's lines and tabs", "one\n\ttwo\nthree", "one\n\ttwo\nthree"},
		{"words from anywhere", "café 日本語 🐎 “quoted” — …", "café 日本語 🐎 “quoted” — …"},
		{"carriage return", "one\r\ntwo\roverwrite", "one\ntwooverwrite"},
		{"colour", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"cursor and erase", "safe\x1b[2J\x1b[H", "safe[2J[H"},
		{"clipboard", "\x1b]52;c;ZXZpbA==\x07", "]52;c;ZXZpbA=="},
		{"window title", "\x1b]0;owned\x1b\\", "]0;owned\\"},
		{"8-bit CSI", "a\u009b31mb", "a31mb"},
		{"backspace", "rm -rf\b\b\b\b\b\bhello", "rm -rfhello"},
		{"bell and null", "a\x07b\x00c", "abc"},
		{"delete", "a\x7fb", "ab"},
		{"text that reads backwards", "a‮b⁦c⁩d‬e", "abcde"},
	} {
		if got := Inert(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Everything a command shows passes through here, so none of them can forget.
func TestEveryReadIsInert(t *testing.T) {
	const bad = `\u001b]52;c;ZXZpbA==\u0007evil\u001b[2J`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/v1/ponies":
			w.Write([]byte(`{"ok":true,"ponies":[{"slug":"p","name":"` + bad + `","doing":"` + bad + `"}]}`))
		case "/api/cli/v1/ponies/p":
			w.Write([]byte(`{"ok":true,"pony":{"slug":"p","name":"` + bad + `","doing":"` + bad + `"}}`))
		case "/api/cli/v1/log":
			w.Write([]byte(`{"ok":true,"events":[{"id":"a","who":"` + bad + `","title":"` + bad + `","text":"` + bad + `"}],"next":"t"}`))
		case "/api/cli/v1/cards/c1":
			w.Write([]byte(`{"ok":true,"card":{"id":"c1","pony":"` + bad + `","state":"idle"},"lines":[{"seq":1,"from":"pony","text":"` + bad + `"}],"next":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "pony_0123456789abcdef0123456789abcdef01234567", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var seen []string
	ponies, err := c.Ponies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, ponies[0].Name, ponies[0].Doing)
	p, err := c.Pony(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, p.Name, p.Doing)
	log, err := c.ReadLog(ctx, "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, log.Events[0].Who, log.Events[0].Title, log.Events[0].Text)
	card, err := c.ReadCard(ctx, "c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, card.Card.Pony, card.Lines[0].Text)
	for _, s := range seen {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Fatalf("a control character got through: %q", s)
		}
		if !strings.Contains(s, "evil") {
			t.Fatalf("the words should stay, harmless: %q", s)
		}
	}
}
