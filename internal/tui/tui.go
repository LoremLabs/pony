package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/auth"
)

// Options is what a conversation needs.
type Options struct {
	Client  *api.Client
	Address string                 // the account, from /me: where the preferences go
	Pony    string                 // who to open on; "" for the last, else the picker
	Prefs   *auth.Prefs            // nil for none
	Plain   bool                   // NO_COLOR
	Open    func(url string) error // opens a page in the person's browser
	Screen  tcell.Screen           // tests give one; nil for the terminal

	onView func(*view) // tests: the screen, once it exists
}

// Result is how it ended, for the line left behind on the terminal.
type Result struct {
	Pony string
	Card string
	Busy bool  // the pony was still at it
	Err  error // what ended it, if something did
}

const keys = `Enter sends. Shift, Ctrl or Alt+Enter starts a new line, if your terminal
reports them. For Option+Enter in Apple's Terminal: Use Option as Meta Key.
PgUp and PgDn scroll. Up on an empty line brings back what you've sent.
Ctrl+O opens this chat in your browser; on the board, the board.
Type / for the commands: arrows move, Tab completes, Enter runs, Esc closes.
Esc, Ctrl+C or Ctrl+D leaves, asking first if something's unsent.

`

// help is the keys and the commands.
func help() string {
	var b strings.Builder
	b.WriteString(keys)
	for _, c := range Commands {
		fmt.Fprintf(&b, "/%-8s %s\n", c.Name, c.About)
	}
	b.WriteString("//text    send a line that starts with a slash")
	return b.String()
}

// session is one sitting. Its goroutine alone reads the server and owns the
// model, so a look and a send can't cross, and a line can't show twice.
type session struct {
	o    Options
	c    *api.Client
	v    *view
	last auth.Last

	slug string
	name string
	pony *api.Pony // as last read
	read time.Time // when
	card string

	onBoard bool // the board's on the screen
	boardAt time.Time
	fresh   bool // a chat with no first line yet, so the card doesn't exist
	m       *Model

	failures int
	failed   struct{ text, key string } // a line that may have landed
	acts     chan func(context.Context)
	result   Result
}

// Run opens the conversation and returns when it's left. Leaving changes
// nothing on the server: what the pony was doing, it's still doing.
func Run(ctx context.Context, o Options) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	v := newView(o.Plain)
	s := &session{o: o, c: o.Client, v: v, m: NewModel(), acts: make(chan func(context.Context), 16)}
	if o.Prefs != nil {
		s.last = o.Prefs.Get(o.Client.Server, o.Address)
	}
	if s.last.Chats == nil {
		s.last.Chats = map[string]string{}
	}
	v.onSubmit = s.submit
	v.onOpen = func() { s.act(s.openBoard) }
	v.onBack = func() { s.act(func(context.Context) { s.onBoard = false }) }
	if o.Screen != nil {
		v.app.SetScreen(o.Screen)
	}
	if o.onView != nil {
		o.onView(v)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.loop(ctx)
	}()
	go func() {
		// A signal to stop, from outside the terminal: leave it as it was found.
		select {
		case <-ctx.Done():
			v.app.Stop()
		case <-v.stopped:
		}
	}()

	if err := v.app.Run(); err != nil {
		s.result.Err = err
	}
	v.stop()
	cancel()
	<-done
	if s.slug != "" {
		s.result.Pony, s.result.Card, s.result.Busy = s.slug, s.card, s.m.Busy()
	}
	return s.result
}

func (s *session) loop(ctx context.Context) {
	s.begin(ctx)
	look := time.NewTimer(0)
	defer look.Stop()
	second := time.NewTicker(time.Second)
	defer second.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case act := <-s.acts:
			act(ctx)
			look.Reset(s.wait())
		case <-look.C:
			s.poll(ctx)
			look.Reset(s.wait())
		case <-second.C:
			if s.m.Busy() {
				s.showStatus()
			}
		}
	}
}

func (s *session) wait() time.Duration {
	wait := s.m.Wait(clock(), s.failures)
	if s.onBoard {
		wait = min(wait, boardEvery)
	}
	return wait
}

// act asks the session's goroutine to do something, from the screen's.
func (s *session) act(f func(context.Context)) {
	select {
	case s.acts <- f:
	default: // a full queue is a person leaning on a key
	}
}

func isStatus(err error, status int) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Status == status
}

func isAuth(err error) bool { return isStatus(err, 401) || isStatus(err, 403) }

// leave ends the sitting with why.
func (s *session) leave(err error) {
	s.result.Err = err
	s.v.app.Stop()
}

func (s *session) note(text string) {
	s.v.do(func() { s.v.note(text) })
}

func (s *session) showStatus() {
	text := s.m.Status(s.name, s.c.Server, s.card, clock())
	if s.failures > 0 {
		text = "Reconnecting…"
	}
	s.v.do(func() { s.v.setStatus(text) })
}

// begin opens on the pony asked for, or the last, or asks.
func (s *session) begin(ctx context.Context) {
	slug := s.o.Pony
	if slug == "" {
		slug = s.last.Pony
	}
	if slug == "" || !s.open(ctx, slug, s.o.Pony != "") {
		s.pick(ctx, true)
	}
}

// pick shows the picker. With no pony open, Esc leaves.
func (s *session) pick(ctx context.Context, first bool) {
	s.onBoard = false
	ponies, err := s.c.Ponies(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.leave(err)
		}
		return
	}
	if len(ponies) == 0 {
		s.leave(errors.New("you have no ponies yet; make one at " + s.c.Server))
		return
	}
	current := s.slug
	if current == "" {
		current = s.last.Pony
	}
	open := s.slug != ""
	s.v.do(func() {
		s.v.showPicker(ponies, first, current, func(slug string) {
			s.act(func(ctx context.Context) {
				if !s.open(ctx, slug, true) {
					s.pick(ctx, false)
				}
			})
		}, func() {
			if open {
				s.v.showChat()
				return
			}
			s.v.app.Stop()
		})
	})
}

// open sits down with a pony, on the chat you were last in with it. False if
// it can't be reached; say is whether that's worth telling you.
func (s *session) open(ctx context.Context, slug string, say bool) bool {
	p, err := s.c.Pony(ctx, slug)
	if err != nil {
		if isAuth(err) {
			s.leave(err)
		} else if say && ctx.Err() == nil {
			s.note(fmt.Sprintf("Couldn't open %s: %v", slug, err))
		}
		return false
	}
	s.slug, s.name, s.pony, s.read = p.Slug, p.Slug, p, clock()
	if p.Name != "" {
		s.name = p.Name
	}
	s.m, s.fresh, s.failures = NewModel(), false, 0
	s.card = s.chooseChat(ctx, slug)

	var r *api.Card
	if !s.fresh {
		r, err = s.c.ReadCard(ctx, s.card, 0)
		if isStatus(err, 404) {
			// A chat since deleted, or one that never got a first line.
			s.card, s.fresh, r, err = api.NewCard(), true, nil, nil
		}
		if err != nil {
			if isAuth(err) {
				s.leave(err)
				return false
			}
			s.note(fmt.Sprintf("Couldn't read this chat yet: %v", err))
		}
	}
	var lines []api.Line
	var held *api.Hold
	if r != nil {
		lines = s.m.Lines(r)
		s.m.Set(r.Card.State, clock())
		held = s.m.SetHold(r.Hold)
	}
	if log, err := s.c.ReadLog(ctx, slug, "", 100); err == nil {
		s.m.SeedEvents(log)
	}
	s.remember()
	link := s.board()
	status := s.m.Status(s.name, s.c.Server, s.card, clock())
	word := placeWord(p, s.m.State)
	s.v.do(func() {
		s.v.clear()
		s.v.setPlace(p, word)
		s.v.composer.SetPlaceholder("Message " + s.name + "…")
		for _, l := range lines {
			s.v.line(pony{p.Slug, s.name}, l)
		}
		if held != nil {
			s.v.hold(held, link)
		}
		s.v.conv.ScrollToEnd()
		s.v.setStatus(status)
		s.v.showChat()
	})
	return true
}

// chooseChat is the chat you were in, else the newest you typed in, else a
// new one. A server without the chats route just means a new one.
func (s *session) chooseChat(ctx context.Context, slug string) string {
	if card := s.last.Chats[slug]; card != "" {
		return card
	}
	chats, err := s.c.Chats(ctx, slug)
	if err == nil {
		for _, c := range chats {
			if c.Via == "web" {
				return c.ID
			}
		}
	}
	s.fresh = true
	return api.NewCard()
}

func (s *session) remember() {
	if s.o.Prefs == nil {
		return
	}
	s.last.Pony = s.slug
	if s.card != "" && !s.fresh {
		s.last.Chats[s.slug] = s.card
	}
	_ = s.o.Prefs.Put(s.c.Server, s.o.Address, s.last) // a convenience: it can fail
}

// showBoard opens the board, or says why it can't: an older server has no such
// route, and that's for here, not a reason to leave.
func (s *session) showBoard(ctx context.Context) {
	lanes, err := s.c.Board(ctx)
	if err != nil {
		if isAuth(err) {
			s.leave(err)
		} else if ctx.Err() == nil {
			s.note(fmt.Sprintf("Couldn't read the board: %v", err))
		}
		return
	}
	s.onBoard, s.boardAt = true, clock()
	s.v.do(func() {
		s.v.setBoard(lanes, "live")
		s.v.showBoard()
	})
}

// watchBoard keeps an open board live. A read that fails keeps what's on the
// screen and says so; the next one puts it right.
func (s *session) watchBoard(ctx context.Context) {
	if !s.onBoard || clock().Sub(s.boardAt) < boardEvery {
		return
	}
	s.boardAt = clock()
	lanes, err := s.c.Board(ctx)
	switch {
	case err == nil:
		s.v.do(func() { s.v.setBoard(lanes, "live") })
	case isAuth(err):
		s.leave(err)
	case ctx.Err() == nil:
		s.v.do(func() { s.v.setBoard(nil, "Reconnecting…") })
	}
}

// poll looks at the chat, and at what the pony's been doing while it's busy.
func (s *session) poll(ctx context.Context) {
	if s.slug == "" {
		return
	}
	s.watchBoard(ctx)
	if s.fresh {
		s.freshPlace(ctx, false) // nothing to read until its first line
		return
	}
	r, err := s.c.ReadCard(ctx, s.card, s.m.After)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return
	case isAuth(err):
		s.leave(err)
		return
	case isStatus(err, 404):
		s.gone(ctx)
		return
	default:
		s.failures++
		s.showStatus()
		return
	}
	was := s.m.Busy()
	s.failures = 0
	lines := s.m.Lines(r)
	s.m.Set(r.Card.State, clock())
	held := s.m.SetHold(r.Hold)

	var events []api.Event
	if was || s.m.Busy() {
		if log, err := s.c.ReadLog(ctx, s.slug, s.m.LogAfter, 100); err == nil {
			events = s.m.Events(log, s.card)
		}
	}
	who := pony{s.slug, s.name}
	link := s.board()
	status := s.m.Status(who.Name, s.c.Server, s.card, clock())
	s.v.do(func() {
		for _, l := range lines {
			s.v.line(who, l)
		}
		for _, e := range events {
			s.v.event(e)
		}
		if held != nil {
			s.v.hold(held, link)
		}
		if len(lines)+len(events) > 0 || held != nil {
			s.v.repaint() // what scrolled off left the same trace a blank does
		}
		s.v.setStatus(status)
	})
	s.freshPlace(ctx, was != s.m.Busy())
}

// freshPlace keeps the word at the top true: straight away when the chat's
// state says, and from a read of the pony now and then, or when work starts
// or stops. A read that fails keeps the last word it had.
func (s *session) freshPlace(ctx context.Context, now bool) {
	if s.pony == nil {
		return
	}
	if now || clock().Sub(s.read) >= placeEvery {
		s.read = clock()
		if p, err := s.c.Pony(ctx, s.slug); err == nil {
			s.pony = p
		}
	}
	p, word := s.pony, placeWord(s.pony, s.m.State)
	s.v.do(func() { s.v.setPlace(p, word) })
}

// board is this chat on the board, where what it's waiting on is decided.
func (s *session) board() string {
	return s.c.Server + "/board?card=" + s.card
}

// openBoard is /open and Ctrl+O: approving happens on the web, so this takes
// you there. Somewhere with no browser, the address does.
func (s *session) openBoard(context.Context) {
	link := s.board()
	if s.onBoard {
		link = s.c.Server + "/board"
	} else if s.slug == "" || s.fresh {
		s.note("There's nothing to open yet.")
		return
	}
	if s.o.Open == nil {
		s.tell("Open this in a browser: " + link)
		return
	}
	if err := s.o.Open(link); err != nil {
		s.tell("Couldn't open a browser here. Open this in one: " + link)
		return
	}
	s.m.Opened(clock())
	s.note("Opened it in your browser.")
}

// tell says something that has to be read, so an open board gives way to it.
func (s *session) tell(text string) {
	if s.onBoard {
		s.onBoard = false
		s.v.do(s.v.showChat)
	}
	s.note(text)
}

// gone is a chat or a pony that isn't there any more.
func (s *session) gone(ctx context.Context) {
	if _, err := s.c.Pony(ctx, s.slug); isStatus(err, 404) {
		s.slug = ""
		s.pick(ctx, false)
		return
	}
	delete(s.last.Chats, s.slug)
	s.newChat()
	s.note("That chat has gone. This is a new one.")
}

func (s *session) newChat() {
	s.card, s.fresh, s.m = api.NewCard(), true, NewModel()
	s.v.do(func() {
		s.v.clear()
		s.v.setStatus("")
	})
}

// submit is the composer's Enter, on the screen's goroutine.
func (s *session) submit(text string) {
	in := ParseInput(text)
	if in.Command != "" {
		s.v.composer.SetText("", false)
		s.command(in.Command)
		return
	}
	if in.Text == "" {
		return
	}
	if msg := tooLong(in.Text); msg != "" {
		s.v.note(msg)
		return
	}
	s.v.composer.SetText("", false)
	s.v.history = append(s.v.history, in.Text)
	s.v.line(pony{}, api.Line{From: "you", Text: in.Text})
	s.v.composer.SetDisabled(true)
	s.v.setStatus("Sending…")
	s.act(func(ctx context.Context) { s.send(ctx, in.Text) })
}

// send puts the line on the chat. One key per line, so a retry can't say it
// twice: and a line that failed keeps its key if it's sent again as it was,
// since it may have landed before the answer was lost.
func (s *session) send(ctx context.Context, text string) {
	key := api.NewKey()
	if s.failed.text == text {
		key = s.failed.key
	}
	said, err := s.c.Say(ctx, s.slug, text, s.card, key)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if isAuth(err) {
			s.leave(err)
			return
		}
		s.failed.text, s.failed.key = text, key
		s.v.do(func() {
			s.v.note("That didn't send: " + sendTrouble(err))
			s.v.composer.SetDisabled(false)
			if s.v.composer.GetTextLength() == 0 {
				s.v.composer.SetText(text, true)
			}
			s.v.setStatus("")
		})
		return
	}
	s.failed.text, s.failed.key = "", ""
	s.fresh = false
	if said.Card != "" {
		s.card = said.Card
	}
	s.m.Sent(said.Seq, clock())
	s.remember()
	status := s.m.Status(s.name, s.c.Server, s.card, clock())
	p, word := s.pony, placeWord(s.pony, s.m.State)
	s.v.do(func() {
		s.v.composer.SetDisabled(false)
		s.v.setStatus(status)
		s.v.setPlace(p, word)
	})
}

// sendTrouble says why, as `pony ask` would.
func sendTrouble(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case "pony_waiting":
			return "this pony is still getting ready. It takes lines once it's let in."
		case "no_such_pony":
			return "this token doesn't reach this pony."
		}
	}
	return err.Error()
}

// command is a slash command, on the screen's goroutine. These are for this
// screen: none of them reach the pony.
func (s *session) command(name string) {
	switch name {
	case "help":
		s.v.note(help())
	case "open":
		s.act(s.openBoard)
	case "exit", "quit":
		s.v.app.Stop()
	case "clear":
		s.v.clear()
	case "ponies":
		s.act(func(ctx context.Context) { s.pick(ctx, false) })
	case "new":
		s.act(func(context.Context) { s.newChat() })
	case "board":
		s.act(s.showBoard)
	case "status":
		s.act(s.statusCommand)
	case "log":
		s.act(s.logCommand)
	default:
		s.v.note(fmt.Sprintf("There's no /%s. /help lists the commands; //%s sends it to the pony.", name, name))
	}
}

func (s *session) statusCommand(ctx context.Context) {
	p, err := s.c.Pony(ctx, s.slug)
	if err != nil {
		s.note(fmt.Sprintf("Couldn't read that: %v", err))
		return
	}
	text := fmt.Sprintf("%s is %s", p.Slug, statusWord(p))
	if p.Doing != "" {
		text += ": " + p.Doing
	}
	if p.Mood != "" {
		text += " (" + p.Mood + ")"
	}
	s.pony, s.read = p, clock()
	word := placeWord(p, s.m.State)
	s.v.do(func() {
		s.v.setPlace(p, word)
		s.v.note(text)
	})
}

func (s *session) logCommand(ctx context.Context) {
	log, err := s.c.ReadLog(ctx, s.slug, "", 10)
	if err != nil {
		s.note(fmt.Sprintf("Couldn't read that: %v", err))
		return
	}
	if len(log.Events) == 0 {
		s.note("Nothing to show yet.")
		return
	}
	lines := make([]string, 0, len(log.Events))
	for _, e := range log.Events {
		lines = append(lines, logTime(e.At)+"  "+e.Who+" "+e.Text)
	}
	s.note(strings.Join(lines, "\n"))
}

// logTime is when a line happened, as `pony log` says it: ISO 8601, in UTC.
func logTime(at string) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return at
	}
	return t.UTC().Format(time.RFC3339)
}
