// Package tui is what bare `pony` opens: a conversation with a pony that's
// already at work somewhere else. It's a client and nothing more. The server
// holds the messages, the cards and the runs, so opening or leaving here
// starts, stops and changes nothing.
package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loremlabs/pony/internal/api"
)

// maxLine is the longest line a pony takes, in characters.
const maxLine = 4000

// How often to look. Quick while an answer is most likely, as `pony ask` is,
// then slower. Idle it's still under the server's 15-second mark that says
// someone is following a card, so a conversation left open counts as someone
// being there, as an open page does, and the answer isn't mailed as well.
// Tests shorten these.
var (
	pollFast = time.Second
	pollSlow = 3 * time.Second
	fastFor  = 20 * time.Second
	pollIdle = 10 * time.Second
	pollMost = 30 * time.Second

	// placeEvery is how often the pony itself is read, for the word at the
	// top: its mood is set a little after a run ends, and it may be busy on
	// another card. Not on every look: that's a heavier read than the card's.
	placeEvery = 30 * time.Second
)

// commandRE is what reads as a command: /word, so a path like /etc/hosts is a
// message that happens to start with a slash.
var commandRE = regexp.MustCompile(`^/[a-z]+$`)

// Input is what the composer held: a command for this screen, or words for
// the pony. Neither, for nothing at all.
type Input struct {
	Command string
	Text    string
}

// ParseInput tells them apart. Commands never reach the pony: //text sends
// /text, for the rare line that really starts that way.
func ParseInput(s string) Input {
	t := strings.TrimSpace(s)
	if t == "" {
		return Input{}
	}
	if strings.HasPrefix(t, "//") {
		return Input{Text: t[1:]}
	}
	if word := strings.Fields(t)[0]; commandRE.MatchString(word) {
		return Input{Command: word[1:]}
	}
	return Input{Text: t}
}

// Command is one of the / commands.
type Command struct {
	Name  string
	About string
}

// Commands are the / commands, in the order the menu offers them.
var Commands = []Command{
	{"help", "the keys and commands"},
	{"open", "open this chat in your browser, to approve what it's waiting on"},
	{"ponies", "talk to another pony"},
	{"new", "start a new chat with this pony"},
	{"status", "what this pony is doing"},
	{"log", "what it's been doing lately"},
	{"clear", "clear this screen, not the conversation"},
	{"exit", "leave; the pony carries on"},
}

// matchCommands is what the / menu offers for what's been typed: only a /word
// at the start of the line, so nothing shows while you're writing a message.
func matchCommands(text string) []Command {
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") || strings.ContainsAny(text, " \t\n") {
		return nil
	}
	var got []Command
	for _, c := range Commands {
		if strings.HasPrefix("/"+c.Name, text) {
			got = append(got, c)
		}
	}
	return got
}

// tooLong says so, or "" for a line a pony can take.
func tooLong(text string) string {
	if n := utf8.RuneCountInString(text); n > maxLine {
		return fmt.Sprintf("That's %d characters; a line to a pony is %d at most.", n, maxLine)
	}
	return ""
}

// activityKinds are what's worth showing between the lines of a conversation:
// what a pony did or what went wrong. The rest would only repeat what it said.
var activityKinds = map[string]bool{
	"effect": true, "waiting": true, "deferred": true, "abandoned": true, "unsent": true,
	"resumed": true, "declined": true,
}

// heldWords are how the log says a hold began: the hold itself says it better.
const heldWords = "stopped for a go-ahead"

// Model is one chat as far as this screen has seen it. It has no terminal in
// it, and only the session's goroutine touches it.
type Model struct {
	After    int    // the card's cursor
	LogAfter string // the log's
	State    string // the card's, as last read
	lines    map[int]bool
	events   map[string]bool
	Hold     *api.Hold // what it's waiting on you to decide
	since    time.Time // when it last became busy
	sent     time.Time // when a line last went
	opened   time.Time // when it was last opened in the browser
	holds    map[string]bool
}

// NewModel is a chat with nothing seen yet.
func NewModel() *Model {
	return &Model{lines: map[int]bool{}, events: map[string]bool{}, holds: map[string]bool{}}
}

// Lines takes a read of the card and gives what's new on it, once each.
func (m *Model) Lines(r *api.Card) []api.Line {
	var fresh []api.Line
	for _, l := range r.Lines {
		if m.lines[l.Seq] {
			continue
		}
		m.lines[l.Seq] = true
		fresh = append(fresh, l)
	}
	if r.Next > m.After {
		m.After = r.Next
	}
	return fresh
}

// Sent notes a line that went: it's on the screen already, so its echo from
// the card isn't shown again, and an answer is worth looking for quickly.
func (m *Model) Sent(seq int, now time.Time) {
	m.lines[seq] = true
	m.sent = now
	m.Set("queued", now)
}

// SeedEvents marks what happened before now as seen: history shows what was
// said, and activity is for what happens while you're here.
func (m *Model) SeedEvents(log *api.Log) {
	for _, e := range log.Events {
		m.events[e.ID] = true
	}
	m.LogAfter = log.Next
}

// Events takes a read of the log and gives what's new and worth showing on
// this card, once each.
func (m *Model) Events(log *api.Log, card string) []api.Event {
	var fresh []api.Event
	for _, e := range log.Events {
		if m.events[e.ID] {
			continue
		}
		m.events[e.ID] = true
		if e.Card == card && activityKinds[e.Kind] && !strings.HasPrefix(e.Text, heldWords) {
			fresh = append(fresh, e)
		}
	}
	if log.Next != "" {
		m.LogAfter = log.Next
	}
	return fresh
}

// SetHold records what the card is held on, and gives it back if it's news:
// each hold is told once, and again only if an approval of it went wrong.
func (m *Model) SetHold(h *api.Hold) *api.Hold {
	m.Hold = h
	if h == nil || m.holds[h.ID+"\n"+h.Note] {
		return nil
	}
	m.holds[h.ID+"\n"+h.Note] = true
	return h
}

// Opened notes that it's gone to the browser: you're likely deciding now, so
// the answer is worth looking for a little sooner for a while.
func (m *Model) Opened(now time.Time) { m.opened = now }

// Set records the card's state.
func (m *Model) Set(state string, now time.Time) {
	if api.Busy(state) && !api.Busy(m.State) {
		m.since = now
	}
	m.State = state
}

// Busy is whether the pony is at it on this chat.
func (m *Model) Busy() bool { return api.Busy(m.State) }

// Wait is how long until the next look.
func (m *Model) Wait(now time.Time, failures int) time.Duration {
	if failures > 0 {
		d := pollFast << min(failures, 5)
		return min(d, pollMost)
	}
	if now.Sub(m.sent) < fastFor {
		return pollFast
	}
	if m.Busy() {
		if now.Sub(m.since) < fastFor {
			return pollFast
		}
		return pollSlow
	}
	if m.State == "needs_you" && now.Sub(m.opened) < 6*fastFor {
		return pollSlow
	}
	return pollIdle
}

// elapsed is how long something has taken, shortly.
func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// Status is the line under the conversation: only what the card says, with a
// board link when it's waiting on the person, since approving happens there.
func (m *Model) Status(pony, server, card string, now time.Time) string {
	switch m.State {
	case "working":
		return fmt.Sprintf("%s is working  %s", pony, elapsed(now.Sub(m.since)))
	case "queued":
		return fmt.Sprintf("%s will get to this next  %s", pony, elapsed(now.Sub(m.since)))
	case "needs_you":
		if m.Hold != nil {
			return "Waiting for your go-ahead. Ctrl+O opens it in your browser."
		}
		return "Waiting on you. Ctrl+O opens it in your browser."
	case "waiting":
		return "Waiting on someone else's answer"
	case "failed":
		return "Its last run failed"
	}
	return ""
}
