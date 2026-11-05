package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

func TestParseInput(t *testing.T) {
	for _, c := range []struct {
		in      string
		command string
		text    string
	}{
		{"", "", ""},
		{"   \n ", "", ""},
		{"hello", "", "hello"},
		{"  two\nlines  ", "", "two\nlines"},
		{"/help", "help", ""},
		{"/ponies", "ponies", ""},
		{"/nope", "nope", ""},
		{"//help", "", "/help"},
		{"// not a command", "", "/ not a command"},
		{"/etc/hosts looks wrong", "", "/etc/hosts looks wrong"},
		{"/usr/bin/env", "", "/usr/bin/env"},
	} {
		got := ParseInput(c.in)
		if got.Command != c.command || got.Text != c.text {
			t.Errorf("%q: got %+v", c.in, got)
		}
	}
}

func TestTooLong(t *testing.T) {
	if tooLong(strings.Repeat("é", maxLine)) != "" {
		t.Fatal("that fits: it's characters, not bytes")
	}
	if tooLong(strings.Repeat("a", maxLine+1)) == "" {
		t.Fatal("that doesn't")
	}
}

func TestLinesOnceEach(t *testing.T) {
	m := NewModel()
	first := m.Lines(&api.Card{Lines: []api.Line{{Seq: 2}, {Seq: 4}}, Next: 4})
	if len(first) != 2 || m.After != 4 {
		t.Fatalf("%v, after %d", first, m.After)
	}
	if again := m.Lines(&api.Card{Lines: []api.Line{{Seq: 4}, {Seq: 5}}, Next: 5}); len(again) != 1 || again[0].Seq != 5 {
		t.Fatalf("%v", again)
	}
	if m.Lines(&api.Card{Next: 1}); m.After != 5 {
		t.Fatalf("the cursor went back: %d", m.After)
	}
}

// What you sent is on the screen already: its echo from the card isn't.
func TestSentLineIsNotShownTwice(t *testing.T) {
	m := NewModel()
	m.Sent(7, time.Now())
	if got := m.Lines(&api.Card{Lines: []api.Line{{Seq: 7, From: "you"}, {Seq: 8, From: "pony"}}, Next: 8}); len(got) != 1 || got[0].Seq != 8 {
		t.Fatalf("%v", got)
	}
}

func TestEventsOnlyThisCardsAndOnlyWhatsWorthShowing(t *testing.T) {
	m := NewModel()
	m.SeedEvents(&api.Log{Events: []api.Event{{ID: "old", Card: "c1", Kind: "effect"}}, Next: "t1"})
	got := m.Events(&api.Log{Events: []api.Event{
		{ID: "old", Card: "c1", Kind: "effect"},  // before you came
		{ID: "a", Card: "c1", Kind: "effect"},    // yes
		{ID: "b", Card: "c1", Kind: "abandoned"}, // yes
		{ID: "c", Card: "c1", Kind: "done"},      // repeats the answer
		{ID: "d", Card: "c1", Kind: "said"},      // a line already
		{ID: "e", Card: "c2", Kind: "effect"},    // another chat
		{ID: "a", Card: "c1", Kind: "effect"},    // again
	}, Next: "t2"}, "c1")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || m.LogAfter != "t2" {
		t.Fatalf("%v, %q", got, m.LogAfter)
	}
	if m.Events(&api.Log{}, "c1"); m.LogAfter != "t2" {
		t.Fatalf("an empty read lost the cursor: %q", m.LogAfter)
	}
}

func TestAHoldSaysItselfBetterThanTheLog(t *testing.T) {
	m := NewModel()
	got := m.Events(&api.Log{Events: []api.Event{
		{ID: "a", Card: "c1", Kind: "waiting", Text: "stopped for a go-ahead — waiting for you"},
		{ID: "b", Card: "c1", Kind: "waiting", Text: "is waiting — on Dana"},
		{ID: "c", Card: "c1", Kind: "resumed", Text: "said: do it"},
	}}, "c1")
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("%v", got)
	}
}

func TestWait(t *testing.T) {
	now := time.Now()
	m := NewModel()
	if got := m.Wait(now, 0); got != pollIdle {
		t.Fatalf("idle: %v", got)
	}
	// Idle must beat the server's mark running out, or an open conversation
	// stops counting as someone being there.
	if pollIdle >= 15*time.Second {
		t.Fatalf("%v is past the 15-second mark", pollIdle)
	}
	m.Sent(1, now)
	if got := m.Wait(now, 0); got != pollFast {
		t.Fatalf("just sent: %v", got)
	}
	m.Set("working", now)
	if got := m.Wait(now.Add(fastFor+time.Second), 0); got != pollSlow {
		t.Fatalf("a long run: %v", got)
	}
	m.Set("idle", now)
	if got := m.Wait(now.Add(time.Hour), 0); got != pollIdle {
		t.Fatalf("done: %v", got)
	}
	last := time.Duration(0)
	for f := 1; f < 10; f++ {
		got := m.Wait(now, f)
		if got < last || got > pollMost {
			t.Fatalf("%d failures: %v", f, got)
		}
		last = got
	}
}

func TestStatus(t *testing.T) {
	now := time.Now()
	m := NewModel()
	if got := m.Status("Pepper", "https://x", "c1", now); got != "" {
		t.Fatalf("%q", got)
	}
	m.Set("working", now.Add(-125*time.Second))
	if got := m.Status("Pepper", "https://x", "c1", now); got != "Pepper is working  2m05s" {
		t.Fatalf("%q", got)
	}
	m.Set("idle", now)
	m.Set("queued", now.Add(-9*time.Second))
	if got := m.Status("Pepper", "https://x", "c1", now); got != "Pepper will get to this next  9s" {
		t.Fatalf("%q", got)
	}
	m.Set("needs_you", now)
	if got := m.Status("Pepper", "https://x", "c1", now); got != "Waiting on you. Ctrl+O opens it in your browser." {
		t.Fatalf("%q", got)
	}
	m.SetHold(&api.Hold{ID: "h"})
	if got := m.Status("Pepper", "https://x", "c1", now); got != "Waiting for your go-ahead. Ctrl+O opens it in your browser." {
		t.Fatalf("%q", got)
	}
}

func TestAHoldIsToldOnceUnlessItWentWrong(t *testing.T) {
	m := NewModel()
	h := &api.Hold{ID: "h", Reason: "r"}
	if m.SetHold(h) == nil {
		t.Fatal("news the first time")
	}
	if m.SetHold(h) != nil {
		t.Fatal("not the second")
	}
	if m.SetHold(&api.Hold{ID: "h", Reason: "r", Note: "it bounced"}) == nil {
		t.Fatal("an approval that went wrong is news")
	}
	if m.SetHold(nil); m.Hold != nil {
		t.Fatal("decided: nothing held")
	}
}

func TestOpeningLooksSoonerForAWhile(t *testing.T) {
	now := time.Now()
	m := NewModel()
	m.Set("needs_you", now)
	if got := m.Wait(now, 0); got != pollIdle {
		t.Fatalf("%v", got)
	}
	m.Opened(now)
	if got := m.Wait(now.Add(time.Second), 0); got != pollSlow {
		t.Fatalf("%v", got)
	}
	if got := m.Wait(now.Add(6*fastFor+time.Second), 0); got != pollIdle {
		t.Fatalf("not for ever: %v", got)
	}
}
