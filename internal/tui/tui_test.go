package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/auth"
)

const testToken = "pony_0123456789abcdef0123456789abcdef01234567"

// fakeServer answers as OnePony does, and keeps what it was told.
type fakeServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	ponies  []api.Pony
	chats   map[string][]api.Chat // pony → its chats
	cards   map[string]*fakeCard
	log     []api.Event
	said    []map[string]string
	sayFail []int // statuses for the next sends
	down    bool
	signOut bool
	noChats bool // an older server
	noBoard bool
	lanes   []api.Lane
	boards  int // reads of the board
	reads   int
}

type fakeCard struct {
	pony  string
	state string
	lines []api.Line
	hold  *api.Hold
}

func newServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, chats: map[string][]api.Chat{}, cards: map[string]*fakeCard{}}
	f.ponies = []api.Pony{
		{Slug: "pepper", Name: "Pepper", Address: "pepper@mail.test", Ready: true, Status: "available", Mood: "resting"},
		{Slug: "juniper", Address: "juniper@mail.test", Ready: true, Status: "working"},
	}
	chat := regexp.MustCompile(`^/api/cli/v1/ponies/([^/]+)/chat$`)
	chats := regexp.MustCompile(`^/api/cli/v1/ponies/([^/]+)/chats$`)
	log := regexp.MustCompile(`^/api/cli/v1/ponies/([^/]+)/log$`)
	one := regexp.MustCompile(`^/api/cli/v1/ponies/([^/]+)$`)
	card := regexp.MustCompile(`^/api/cli/v1/cards/([^/]+)$`)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		send := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(v)
		}
		no := func(status int, code string) {
			send(status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": strings.ReplaceAll(code, "_", " ")}})
		}
		if f.down {
			no(503, "unavailable")
			return
		}
		if f.signOut {
			no(401, "not_signed_in")
			return
		}
		p := r.URL.Path
		switch {
		case p == "/api/cli/v1/ponies":
			send(200, map[string]any{"ok": true, "ponies": f.ponies})
		case p == "/api/cli/v1/board":
			if f.noBoard {
				no(404, "not_found")
				return
			}
			f.boards++
			send(200, map[string]any{"ok": true, "lanes": f.lanes})
		case chat.MatchString(p):
			if len(f.sayFail) > 0 {
				status := f.sayFail[0]
				f.sayFail = f.sayFail[1:]
				no(status, "pony_waiting")
				return
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			body["key"] = r.Header.Get("Idempotency-Key")
			f.said = append(f.said, body)
			id := body["card"]
			c := f.cards[id]
			if c == nil {
				c = &fakeCard{pony: chat.FindStringSubmatch(p)[1], state: "idle"}
				f.cards[id] = c
			}
			seq := len(c.lines) + 2
			c.lines = append(c.lines, api.Line{Seq: seq, From: "you", Text: body["text"]})
			c.state = "working"
			send(200, map[string]any{"ok": true, "card": id, "seq": seq})
		case chats.MatchString(p):
			if f.noChats {
				no(404, "not_found")
				return
			}
			send(200, map[string]any{"ok": true, "chats": f.chats[chats.FindStringSubmatch(p)[1]]})
		case log.MatchString(p):
			send(200, map[string]any{"ok": true, "events": f.log, "next": "t" + strconv.Itoa(len(f.log))})
		case one.MatchString(p):
			for _, q := range f.ponies {
				if q.Slug == one.FindStringSubmatch(p)[1] {
					send(200, map[string]any{"ok": true, "pony": q})
					return
				}
			}
			no(404, "no_such_pony")
		case card.MatchString(p):
			c := f.cards[card.FindStringSubmatch(p)[1]]
			if c == nil {
				no(404, "no_such_card")
				return
			}
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			lines := []api.Line{}
			next := after
			for _, l := range c.lines {
				if l.Seq > after {
					lines = append(lines, l)
					next = l.Seq
				}
			}
			reply := map[string]any{"ok": true, "card": map[string]string{"id": "x", "pony": c.pony, "state": c.state}, "lines": lines, "next": next}
			if c.hold != nil {
				reply["hold"] = c.hold
			}
			send(200, reply)
		default:
			no(404, "not_found")
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeServer) answer(card, text string) {
	f.with(func() {
		c := f.cards[card]
		c.lines = append(c.lines, api.Line{Seq: len(c.lines) + 2, From: "pony", Text: text})
		c.state = "idle"
	})
}

func (f *fakeServer) lastCard() string {
	var id string
	f.with(func() {
		if len(f.said) > 0 {
			id = f.said[len(f.said)-1]["card"]
		}
	})
	return id
}

// sitting is a conversation running on a simulated screen.
type sitting struct {
	t      *testing.T
	screen tcell.SimulationScreen
	done   chan Result
	cancel context.CancelFunc
	viewCh chan *view
	v      *view
}

// TestMain makes every sitting quick: one setting, since a test can hold two.
func TestMain(m *testing.M) {
	pollFast, pollSlow, pollIdle, pollMost = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond
	placeEvery, boardEvery = 10*time.Millisecond, 10*time.Millisecond
	os.Exit(m.Run())
}

func sit(t *testing.T, f *fakeServer, o Options, width, height int) *sitting {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	c, err := api.New(f.srv.URL, testToken, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	o.Client, o.Screen = c, screen
	viewCh := make(chan *view, 1)
	o.onView = func(v *view) { viewCh <- v }
	if o.Address == "" {
		o.Address = "ada@example.com"
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &sitting{t: t, screen: screen, done: make(chan Result, 1), cancel: cancel, viewCh: viewCh}
	go func() { s.done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
		}
	})
	// tview starts the screen it's given, which resets it to 80 by 25, so the
	// size can only be set once it's running.
	running := make(chan struct{})
	v := s.view()
	go func() {
		v.app.QueueUpdate(func() {})
		close(running)
	}()
	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("it never started")
	}
	screen.SetSize(width, height)
	screen.PostEventWait(tcell.NewEventResize(width, height))
	return s
}

// text is what's on the screen, row by row, read on tview's goroutine so it
// can't be caught mid-draw.
func (s *sitting) text() string {
	v := s.view()
	var b strings.Builder
	read := func() {
		cells, w, h := s.screen.GetContents()
		for y := range h {
			for x := range w {
				c := cells[y*w+x]
				if len(c.Bytes) == 0 {
					b.WriteByte(' ')
				} else {
					b.Write(c.Bytes)
				}
			}
			b.WriteByte('\n')
		}
	}
	done := make(chan struct{})
	go func() {
		v.app.QueueUpdate(read)
		close(done)
	}()
	select {
	case <-done:
	case <-v.stopped:
		return ""
	case <-time.After(5 * time.Second):
		return ""
	}
	return b.String()
}

// cells is the screen's cells, read on tview's goroutine.
func (s *sitting) cells() []tcell.SimCell {
	v := s.view()
	var got []tcell.SimCell
	done := make(chan struct{})
	go func() {
		v.app.QueueUpdate(func() {
			cells, _, _ := s.screen.GetContents()
			got = append(got, cells...)
		})
		close(done)
	}()
	select {
	case <-done:
	case <-v.stopped:
	case <-time.After(5 * time.Second):
	}
	return got
}

func (s *sitting) view() *view {
	select {
	case v := <-s.viewCh:
		s.v = v
	default:
	}
	for s.v == nil {
		s.v = <-s.viewCh
	}
	return s.v
}

func (s *sitting) see(want string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.text(), want) {
		if time.Now().After(deadline) {
			s.t.Fatalf("never saw %q on:\n%s", want, s.text())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *sitting) gone(what string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Contains(s.text(), what) {
		if time.Now().After(deadline) {
			s.t.Fatalf("%q stayed on:\n%s", what, s.text())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *sitting) typeText(text string) {
	for _, r := range text {
		s.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	time.Sleep(20 * time.Millisecond)
}

func (s *sitting) key(k tcell.Key) { s.screen.InjectKey(k, 0, tcell.ModNone) }

func (s *sitting) paste(text string) {
	s.screen.PostEventWait(tcell.NewEventPaste(true))
	for _, r := range text {
		if r == '\n' {
			s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			continue
		}
		s.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	s.screen.PostEventWait(tcell.NewEventPaste(false))
	time.Sleep(20 * time.Millisecond)
}

func (s *sitting) left() Result {
	s.t.Helper()
	select {
	case r := <-s.done:
		s.done <- r
		return r
	case <-time.After(5 * time.Second):
		s.t.Fatalf("it never left:\n%s", s.text())
		return Result{}
	}
}

func prefsIn(t *testing.T) *auth.Prefs {
	t.Helper()
	return &auth.Prefs{Path: filepath.Join(t.TempDir(), "prefs.json")}
}

func TestPickerThenConversation(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{}, 80, 24)
	s.see("Welcome to OnePony.")
	s.see("pepper")
	s.see("resting")
	s.see("juniper")
	s.key(tcell.KeyEnter)
	s.see("Message Pepper…")
	s.typeText("what time is it?")
	s.see("what time is it?")
	s.key(tcell.KeyEnter)
	s.see("- what time is it?")
	s.see("Pepper is working")
	s.gone("Message Pepper…x")
	s.gone("what time is it?x")

	f.answer(s.waitCard(f), "It is 14:53 in Paris.")
	s.see("It is 14:53 in Paris.")
	s.gone("Pepper is working")

	var said []map[string]string
	f.with(func() { said = append(said, f.said...) })
	if len(said) != 1 || said[0]["text"] != "what time is it?" || said[0]["card"] == "" {
		t.Fatalf("%+v", said)
	}
}

func (s *sitting) waitCard(f *fakeServer) string {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if id := f.lastCard(); id != "" {
			return id
		}
		if time.Now().After(deadline) {
			s.t.Fatal("nothing was sent")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPonyGivenOpensOnIt(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "juniper"}, 80, 24)
	s.see("Message juniper…")
	if strings.Contains(s.text(), "Welcome") {
		t.Fatalf("no picker when a pony's named:\n%s", s.text())
	}
}

func TestReturnsToTheLastPonyAndChat(t *testing.T) {
	f := newServer(t)
	f.cards["wch_old"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "you", Text: "an old question"}, {Seq: 3, From: "pony", Text: "an old answer"}}}
	prefs := prefsIn(t)
	if err := prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_old"}}); err != nil {
		t.Fatal(err)
	}
	s := sit(t, f, Options{Prefs: prefs}, 80, 24)
	s.see("an old answer")
	s.see("an old question")
	// Coming back shows it once, however many looks go by.
	time.Sleep(60 * time.Millisecond)
	if n := strings.Count(s.text(), "an old answer"); n != 1 {
		t.Fatalf("shown %d times:\n%s", n, s.text())
	}
}

func TestAnotherAccountStartsAtThePicker(t *testing.T) {
	f := newServer(t)
	prefs := prefsIn(t)
	if err := prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper"}); err != nil {
		t.Fatal(err)
	}
	s := sit(t, f, Options{Prefs: prefs, Address: "bob@example.com"}, 80, 24)
	s.see("Welcome to OnePony.")
}

func TestOpensOnTheNewestChatItTypedIn(t *testing.T) {
	f := newServer(t)
	f.chats["pepper"] = []api.Chat{{ID: "mcd_mail", Via: "mail"}, {ID: "wch_typed", Via: "web"}}
	f.cards["wch_typed"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "pony", Text: "from the typed chat"}}}
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("from the typed chat")
}

func TestAnOlderServerJustStartsANewChat(t *testing.T) {
	f := newServer(t)
	f.noChats = true
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("hello")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
}

// Nothing a reply says can act on the terminal, or dress up as this screen.
func TestRemoteTextIsInert(t *testing.T) {
	f := newServer(t)
	evil := "fine \x1b]52;c;ZXZpbA==\x07[red]not red[-] [::b]x [\"region\"]y\x1b[2J done"
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "pony", Text: evil}}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 100, 24)
	s.see("[red]not red[-]")
	s.see("[::b]x")
	s.see(`["region"]y`)
	s.see("done")
	if strings.Contains(s.text(), "\x1b") {
		t.Fatalf("an escape reached the screen:\n%q", s.text())
	}
}

func TestMultilinePasteDoesNotSend(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.paste("first line\nsecond line\nthird line")
	s.see("third line")
	s.see("first line")
	var n int
	f.with(func() { n = len(f.said) })
	if n != 0 {
		t.Fatalf("a paste sent %d lines", n)
	}
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if got := f.said[0]["text"]; got != "first line\nsecond line\nthird line" {
			t.Fatalf("%q", got)
		}
	})
}

func TestNewLineKeysDoNotSend(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("one")
	s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModShift)
	s.typeText("two")
	s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModAlt)
	s.typeText("three")
	s.see("three")
	var n int
	f.with(func() { n = len(f.said) })
	if n != 0 {
		t.Fatal("a new line sent it")
	}
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if got := f.said[0]["text"]; got != "one\ntwo\nthree" {
			t.Fatalf("%q", got)
		}
	})
}

func TestHelpFitsAnOrdinaryTerminal(t *testing.T) {
	for _, line := range strings.Split(help(), "\n") {
		if w := runewidth.StringWidth(line); w > 80-2*sidePadding {
			t.Errorf("%d wide: %q", w, line)
		}
	}
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/help")
	s.key(tcell.KeyEnter)
	s.see("Enter sends.")
	s.see("/board")
	s.see("//text")
}

func TestCommandsNeverReachThePony(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/help")
	s.key(tcell.KeyEnter)
	s.see("/ponies")
	s.typeText("/nope")
	s.key(tcell.KeyEnter)
	s.see("There's no /nope")
	s.typeText("/status")
	s.key(tcell.KeyEnter)
	s.see("pepper is available")
	var n int
	f.with(func() { n = len(f.said) })
	if n != 0 {
		t.Fatalf("%d commands were sent", n)
	}
	s.typeText("//help")
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if got := f.said[0]["text"]; got != "/help" {
			t.Fatalf("%q", got)
		}
	})
}

func TestNewChatAndClear(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("first chat")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	first := s.waitCard(f)
	f.answer(first, "first answer")
	s.see("first answer")

	s.typeText("/clear")
	s.key(tcell.KeyEnter)
	s.gone("first answer")

	s.typeText("/new")
	s.key(tcell.KeyEnter)
	time.Sleep(30 * time.Millisecond)
	s.typeText("second chat")
	s.key(tcell.KeyEnter)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if id := f.lastCard(); id != first && id != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("it should have started another chat")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSwitchingPonies(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/ponies")
	s.key(tcell.KeyEnter)
	s.see("Which pony would you like to talk to?")
	if strings.Contains(s.text(), "Welcome") {
		t.Fatalf("not a first run:\n%s", s.text())
	}
	s.key(tcell.KeyDown)
	s.key(tcell.KeyEnter)
	s.see("Message juniper…")
}

func TestEscFromPickerGoesBackOrLeaves(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/ponies")
	s.key(tcell.KeyEnter)
	s.see("Which pony")
	s.key(tcell.KeyEscape)
	s.see("Message Pepper…")

	first := sit(t, f, Options{}, 80, 24)
	first.see("Welcome")
	first.key(tcell.KeyEscape)
	if r := first.left(); r.Pony != "" || r.Err != nil {
		t.Fatalf("%+v", r)
	}
}

// A send that failed comes back to you, and sending it again as it was can't
// say it twice.
func TestAFailedSendComesBackUnderTheSameKey(t *testing.T) {
	f := newServer(t)
	f.sayFail = []int{409}
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("important")
	s.key(tcell.KeyEnter)
	s.see("That didn't send")
	s.see("still getting ready")
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if len(f.said) != 1 || f.said[0]["text"] != "important" {
			t.Fatalf("%+v", f.said)
		}
	})
}

func TestReconnects(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("are you there?")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	card := s.waitCard(f)

	f.with(func() { f.down = true })
	s.see("Reconnecting…")
	f.with(func() { f.down = false })
	f.answer(card, "back again")
	s.see("back again")
	s.gone("Reconnecting…")
	if n := strings.Count(s.text(), "back again"); n != 1 {
		t.Fatalf("shown %d times:\n%s", n, s.text())
	}
}

func TestSignedOutLeaves(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("hi")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	f.with(func() { f.signOut = true })
	r := s.left()
	if r.Err == nil || !isStatus(r.Err, 401) {
		t.Fatalf("%+v", r)
	}
}

func TestPonyThatWentAwayGoesToThePicker(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("hi")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	card := s.waitCard(f)
	f.with(func() {
		delete(f.cards, card)
		f.ponies = f.ponies[1:]
	})
	s.see("Which pony")
}

func TestLeavingDoesNotTouchTheServer(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("do a long thing")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	card := s.waitCard(f)
	s.typeText("/exit")
	s.key(tcell.KeyEnter)
	r := s.left()
	if r.Pony != "pepper" || r.Card != card || !r.Busy || r.Err != nil {
		t.Fatalf("%+v", r)
	}
	f.with(func() {
		if len(f.said) != 1 || f.cards[card].state != "working" {
			t.Fatalf("leaving changed something: %+v %s", f.said, f.cards[card].state)
		}
	})
}

func TestEveryWayOutAsksBeforeLosingADraft(t *testing.T) {
	for _, k := range []tcell.Key{tcell.KeyCtrlC, tcell.KeyCtrlD, tcell.KeyEscape} {
		f := newServer(t)
		s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
		s.see("Message Pepper…")
		s.typeText("half a thought")
		s.key(k)
		s.see("Press that again to leave")
		select {
		case <-s.done:
			t.Fatalf("%v left with a draft unsent", k)
		case <-time.After(30 * time.Millisecond):
		}
		s.see("half a thought") // and it's still there
		s.key(k)
		s.left()
	}
}

func TestEveryWayOutLeavesAtOnceWithNoDraft(t *testing.T) {
	for _, k := range []tcell.Key{tcell.KeyCtrlC, tcell.KeyCtrlD, tcell.KeyEscape} {
		f := newServer(t)
		s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
		s.see("Message Pepper…")
		s.key(k)
		if r := s.left(); r.Pony != "pepper" || r.Err != nil {
			t.Fatalf("%v: %+v", k, r)
		}
	}
}

func TestTypingAfterAWarningAsksAgain(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("half")
	s.key(tcell.KeyEscape)
	s.see("Press that again")
	s.typeText(" more")
	s.key(tcell.KeyEscape)
	select {
	case <-s.done:
		t.Fatal("it left without asking about what changed")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestLeavingThePicker(t *testing.T) {
	for _, k := range []tcell.Key{tcell.KeyCtrlC, tcell.KeyCtrlD, tcell.KeyEscape} {
		f := newServer(t)
		s := sit(t, f, Options{}, 80, 24)
		s.see("Esc quit")
		s.key(k)
		if r := s.left(); r.Pony != "" || r.Err != nil {
			t.Fatalf("%v: %+v", k, r)
		}
	}
}

func TestPickerFromAConversationSaysEscGoesBack(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/ponies")
	s.key(tcell.KeyEnter)
	s.see("Esc back")
	s.key(tcell.KeyCtrlD)
	s.left()
}

func TestUpRecallsOnlyFromAnEmptyComposer(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("the first thing")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	f.answer(s.waitCard(f), "ok")
	s.see("ok")
	s.key(tcell.KeyUp)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(s.text(), "the first thing") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Up brought nothing back:\n%s", s.text())
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.key(tcell.KeyDown)
	for strings.Count(s.text(), "the first thing") > 1 {
		if time.Now().After(deadline) {
			t.Fatalf("Down should clear it:\n%s", s.text())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestComposerGrows(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.paste("row-a\nrow-b\nrow-c")
	s.see("row-a") // it grows to show them all
	s.see("row-c")
	s.paste("\nrow-d\nrow-e\nrow-f\nrow-g\nrow-h\nrow-i")
	s.see("row-i")
	s.see("row-d") // up to six rows, the last of them
	s.gone("row-c")
	// What's above it is still on the screen.
	s.see("OnePony")
}

func TestSmallAndResizedScreens(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 24, 8)
	s.see("Message")
	s.typeText("a line that is much wider than this narrow little screen")
	s.key(tcell.KeyEnter)
	s.see("working")
	s.screen.SetSize(100, 30)
	s.screen.PostEventWait(tcell.NewEventResize(100, 30))
	s.see("a line that is much wider than this narrow little screen")
}

func TestUsesTheTerminalsOwnBackground(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	for _, c := range s.cells() {
		_, bg, _ := c.Style.Decompose()
		if bg != tcell.ColorDefault {
			t.Fatalf("painted a background: %v", bg)
		}
	}
}

func TestPrefsRememberThePonyAndChat(t *testing.T) {
	f := newServer(t)
	prefs := prefsIn(t)
	s := sit(t, f, Options{Pony: "pepper", Prefs: prefs}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("remember me")
	s.key(tcell.KeyEnter)
	s.see("Pepper is working")
	card := s.waitCard(f)
	deadline := time.Now().Add(5 * time.Second)
	for prefs.Get(f.srv.URL, "ada@example.com").Chats["pepper"] != card {
		if time.Now().After(deadline) {
			t.Fatalf("%+v", prefs.Get(f.srv.URL, "ada@example.com"))
		}
		time.Sleep(2 * time.Millisecond)
	}
	b, _ := os.ReadFile(prefs.Path)
	if strings.Contains(string(b), "pony_") {
		t.Fatalf("a token in the preferences: %s", b)
	}
}

func (s *sitting) row(want string) string {
	s.t.Helper()
	s.see(want)
	for _, l := range strings.Split(s.text(), "\n") {
		if strings.Contains(l, want) {
			return l
		}
	}
	return ""
}

func TestThePromptAndTheRule(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	if got := s.row("Message Pepper…"); !strings.Contains(got, "> Message Pepper…") {
		t.Fatalf("no prompt: %q", got)
	}
	rows := strings.Split(s.text(), "\n")
	at := -1
	for i, l := range rows {
		if strings.Contains(l, "> Message") {
			at = i
		}
	}
	if at < 1 || strings.Count(rows[at-1], "─") < 70 {
		t.Fatalf("no solid line above what you type: %q", rows[at-1])
	}
	s.typeText("hello")
	if got := s.row("hello"); !strings.Contains(got, "> hello") {
		t.Fatalf("the prompt should stay: %q", got)
	}
}

func TestAMultilineDraftWrapsUnderItsPrompt(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.paste("one\ntwo")
	s.see("two")
	if got := s.row("two"); !strings.Contains(got, "  two") || strings.Contains(got, "> two") {
		t.Fatalf("one prompt, on the first line: %q", got)
	}
}

func TestAnAnswerHasItsBarAndAddress(t *testing.T) {
	f := newServer(t)
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "you", Text: "a question"}, {Seq: 3, From: "pony", Text: "an answer"}, {Seq: 4, From: "pony", Text: "and another"}}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 80, 24)
	s.see("┃ and another")
	s.see("┃ an answer")
	s.see("- a question")
	screen := s.text()
	// The pony you're talking to needs no name, and you need no bar.
	if strings.Contains(screen, "Pepper\n") || strings.Contains(screen, "┃ Pepper") || strings.Contains(screen, "You") {
		t.Fatalf("a name where a bar will do:\n%s", screen)
	}
	if strings.Contains(screen, "┃ a question") || strings.Contains(screen, "- an answer") {
		t.Fatalf("the wrong mark:\n%s", screen)
	}
}

func TestEveryRowOfATurnCarriesItsMark(t *testing.T) {
	f := newServer(t)
	long := strings.TrimSpace(strings.Repeat("a word that wraps ", 12))
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{
		{Seq: 2, From: "you", Text: long + "\n\nand a second paragraph"},
		{Seq: 3, From: "pony", Text: long + "\n\n- a list\n- of things"},
	}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 60, 30)
	s.see("┃ - of things")
	s.see("- and a second paragraph")
	var you, answer int
	for _, l := range strings.Split(s.text(), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- ") || t == "-" {
			you++
		}
		if strings.HasPrefix(t, "┃") {
			answer++
		}
	}
	if you < 5 || answer < 6 {
		t.Fatalf("wrapped rows lost their mark (%d, %d):\n%s", you, answer, s.text())
	}
	// Nothing runs past the screen.
	for _, l := range strings.Split(s.text(), "\n") {
		if strings.Contains(l, "word") && !strings.HasPrefix(strings.TrimSpace(l), "-") && !strings.HasPrefix(strings.TrimSpace(l), "┃") {
			t.Fatalf("a row with no mark: %q\n%s", l, s.text())
		}
	}
}

func TestSomeoneElseIsNamedWithTheirWordsSetIn(t *testing.T) {
	f := newServer(t)
	long := strings.TrimSpace(strings.Repeat("words that wrap ", 12))
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "dana@example.test", Text: long}}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 60, 24)
	s.see("dana@example.test")
	rows := strings.Split(s.text(), "\n")
	at := -1
	for i, l := range rows {
		if strings.Contains(l, "dana@example.test") {
			at = i
		}
	}
	for _, l := range rows[at+1 : at+3] {
		if !strings.HasPrefix(l, "  "+otherIndent) || l[5] == ' ' {
			t.Fatalf("set in %d, want three: %q\n%s", len(l)-len(strings.TrimLeft(l, " "))-2, l, s.text())
		}
	}
}

func TestAResizeWrapsItAgain(t *testing.T) {
	f := newServer(t)
	long := strings.TrimSpace(strings.Repeat("rewrap me please ", 9))
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "pony", Text: long}}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 50, 24)
	s.see("┃ rewrap me please")
	count := func() int { return strings.Count(s.text(), "┃") }
	narrow := count()
	if narrow < 3 {
		t.Fatalf("%d rows at 50 columns:\n%s", narrow, s.text())
	}
	s.screen.SetSize(120, 24)
	s.screen.PostEventWait(tcell.NewEventResize(120, 24))
	deadline := time.Now().Add(5 * time.Second)
	for count() >= narrow {
		if time.Now().After(deadline) {
			t.Fatalf("it never wrapped again:\n%s", s.text())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWrapLine(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  []string
	}{
		{"short", 10, []string{"short"}},
		{"", 10, []string{""}},
		{"one two three", 7, []string{"one two", "three"}},
		{"  - indented item here", 12, []string{"  - indented", "item here"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"日本語日本語", 6, []string{"日本語", "日本語"}},
		{"ok abcdefghij", 4, []string{"ok", "abcd", "efgh", "ij"}},
	} {
		got := wrapLine(c.in, c.width)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q at %d: got %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestTheHeaderHasTheAddress(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("pepper <pepper@mail.test>  resting")
	// With a line under it.
	rows := strings.Split(s.text(), "\n")
	if strings.Count(rows[1], "─") < 70 {
		t.Fatalf("no line under the top bar: %q", rows[1])
	}
}

func TestNoHintsAtTheBottom(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("> Message Pepper…")
	if strings.Contains(s.text(), "Enter send") {
		t.Fatalf("the keys are in /help:\n%s", s.text())
	}
	rows := strings.Split(strings.TrimRight(s.text(), "\n"), "\n")
	if !strings.Contains(rows[len(rows)-1], "> Message Pepper…") {
		t.Fatalf("what you type is the last row:\n%s", s.text())
	}
}

func TestCtrlJIsNotANewLine(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("one")
	s.key(tcell.KeyCtrlJ)
	s.typeText("two")
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if got := f.said[0]["text"]; got != "onetwo" {
			t.Fatalf("%q", got)
		}
	})
}

func TestABackslashIsJustABackslash(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText(`C:\temp\`)
	s.key(tcell.KeyEnter)
	s.waitCard(f)
	f.with(func() {
		if got := f.said[0]["text"]; got != `C:\temp\` {
			t.Fatalf("%q", got)
		}
	})
}

func TestEveryNewLineKeyDoesTheSame(t *testing.T) {
	for name, press := range map[string]func(*sitting){
		"Shift+Enter": func(s *sitting) { s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModShift) },
		"Ctrl+Enter":  func(s *sitting) { s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModCtrl) },
		"Alt+Enter":   func(s *sitting) { s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModAlt) },
		"Meta+Enter":  func(s *sitting) { s.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModMeta) },
	} {
		f := newServer(t)
		s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
		s.see("Message Pepper…")
		s.typeText("one")
		press(s)
		s.typeText("two")
		s.see("two")
		var n int
		f.with(func() { n = len(f.said) })
		if n != 0 {
			t.Fatalf("%s sent it", name)
		}
		s.key(tcell.KeyEnter)
		s.waitCard(f)
		f.with(func() {
			if got := f.said[0]["text"]; got != "one\ntwo" {
				t.Fatalf("%s: %q", name, got)
			}
		})
	}
}

func TestThePickerHasColourAddressesAndAnArrow(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{}, 80, 30)
	first := s.row("<pepper@mail.test>")
	if !strings.Contains(first, "❯ pepper") || !strings.Contains(first, "resting") {
		t.Fatalf("%q", first)
	}
	if got := s.row("<juniper@mail.test>"); strings.Contains(got, "❯") {
		t.Fatalf("the arrow is for the one you're on: %q", got)
	}
	s.key(tcell.KeyDown)
	s.see("❯ juniper")
	if got := s.row("<pepper@mail.test>"); strings.Contains(got, "❯") {
		t.Fatalf("the arrow should have moved: %q", got)
	}
	// The name is in its colour.
	found := false
	for _, c := range s.cells() {
		fg, _, _ := c.Style.Decompose()
		if len(c.Bytes) > 0 && c.Bytes[0] == 'j' && fg != tcell.ColorDefault {
			found = true
		}
	}
	if !found {
		t.Fatal("no colour on the screen")
	}
}

func ponyCells(s *sitting) int {
	n := 0
	for _, c := range s.cells() {
		if _, bg, _ := c.Style.Decompose(); bg != tcell.ColorDefault {
			n++
		}
	}
	return n
}

func TestThePickerHasThePony(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{}, 80, 30)
	s.see("Welcome to OnePony.")
	deadline := time.Now().Add(5 * time.Second)
	for ponyCells(s) < 80 {
		if time.Now().After(deadline) {
			t.Fatalf("no pony: %d cells", ponyCells(s))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The list is what the page is for: a short terminal keeps it, not the pony.
func TestThePonyGivesWayOnAShortTerminal(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{}, 80, 12)
	s.see("Welcome to OnePony.")
	s.see("<juniper@mail.test>")
	if n := ponyCells(s); n != 0 {
		t.Fatalf("%d cells of pony on a short terminal", n)
	}
}

func TestNoColourMeansNoPony(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Plain: true}, 80, 30)
	s.see("Welcome to OnePony.")
	s.see("<pepper@mail.test>")
	if n := ponyCells(s); n != 0 {
		t.Fatalf("%d cells of colour under NO_COLOR", n)
	}
}

// held is a sitting on a chat that's waiting for a go-ahead.
func held(t *testing.T, f *fakeServer, o Options, h *api.Hold) *sitting {
	t.Helper()
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "needs_you", hold: h, lines: []api.Line{{Seq: 2, From: "pony", Text: "I need to ask."}}}
	o.Prefs = prefsIn(t)
	o.Prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	return sit(t, f, o, 90, 30)
}

func TestAHoldSaysWhatItsWaitingFor(t *testing.T) {
	f := newServer(t)
	s := held(t, f, Options{}, &api.Hold{ID: "h1", Name: "mcp.call", Reason: "a call made in their name: ask_wiki_question on deepwiki"})
	s.see("Waiting for your go-ahead")
	s.see("a call made in your name: ask_wiki_question on deepwiki")
	s.see("Ctrl+O opens it in your browser")
	s.see("/board?card=wch_a")
}

func TestAHoldSaysWhatWentWrong(t *testing.T) {
	f := newServer(t)
	s := held(t, f, Options{}, &api.Hold{ID: "h1", Name: "mail.send", Reason: "a mail to a stranger", Note: "the server refused it"})
	s.see("It didn't go last time: the server refused it")
}

func TestAHoldIsToldOnceAndClearsWhenDecided(t *testing.T) {
	f := newServer(t)
	s := held(t, f, Options{}, &api.Hold{ID: "h1", Name: "mail.send", Reason: "a mail to a stranger"})
	s.see("a mail to a stranger")
	time.Sleep(60 * time.Millisecond)
	if n := strings.Count(s.text(), "a mail to a stranger"); n != 1 {
		t.Fatalf("told %d times:\n%s", n, s.text())
	}
	s.see("Waiting for your go-ahead. Ctrl+O")
	f.with(func() {
		c := f.cards["wch_a"]
		c.hold, c.state = nil, "working"
		f.log = append(f.log, api.Event{ID: "e1", Card: "wch_a", Pony: "pepper", Who: "you", Kind: "resumed", Title: "t", Text: "said: do it · t"})
	})
	s.see("Pepper is working")
	s.gone("Waiting for your go-ahead. Ctrl+O")
	s.see("you said: do it")
	if strings.Contains(s.text(), "· t") {
		t.Fatalf("the chat's title, repeated:\n%s", s.text())
	}
}

func TestAQuestionIsNotAGoAhead(t *testing.T) {
	f := newServer(t)
	s := held(t, f, Options{}, nil)
	s.see("Waiting on you. Ctrl+O")
	if strings.Contains(s.text(), "go-ahead") {
		t.Fatalf("%s", s.text())
	}
}

func TestOpeningInTheBrowser(t *testing.T) {
	for name, press := range map[string]func(*sitting){
		"Ctrl+O": func(s *sitting) { s.key(tcell.KeyCtrlO) },
		"/open":  func(s *sitting) { s.typeText("/open"); s.key(tcell.KeyEnter) },
	} {
		f := newServer(t)
		opened := make(chan string, 1)
		s := held(t, f, Options{Open: func(u string) error { opened <- u; return nil }}, &api.Hold{ID: "h1", Name: "mail.send", Reason: "r"})
		s.see("Ctrl+O opens it")
		press(s)
		select {
		case got := <-opened:
			if got != f.srv.URL+"/board?card=wch_a" {
				t.Fatalf("%s: %q", name, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s opened nothing", name)
		}
		s.see("Opened it in your browser.")
		var n int
		f.with(func() { n = len(f.said) })
		if n != 0 {
			t.Fatalf("%s reached the pony", name)
		}
	}
}

func TestNoBrowserGivesTheAddress(t *testing.T) {
	f := newServer(t)
	s := held(t, f, Options{Open: func(string) error { return errors.New("no browser here") }}, &api.Hold{ID: "h1", Name: "mail.send", Reason: "r"})
	s.see("Ctrl+O opens it")
	s.key(tcell.KeyCtrlO)
	s.see("Couldn't open a browser here.")
}

func TestNothingToOpenInANewChat(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper", Open: func(string) error { t.Error("opened a chat that doesn't exist yet"); return nil }}, 80, 24)
	s.see("Message Pepper…")
	s.key(tcell.KeyCtrlO)
	s.see("There's nothing to open yet.")
}

func TestTheSlashMenu(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/")
	for _, c := range Commands {
		s.see("/" + c.Name)
	}
	s.see(Commands[0].About)
	s.typeText("p")
	s.see("❯ /ponies")
	s.gone("/status")
	// What you're writing isn't a command.
	s.typeText(" not a command")
	s.gone("❯ /ponies")
}

func TestTheSlashMenuOnlyOpensAtTheStart(t *testing.T) {
	for _, text := range []string{"a /", "//"} {
		f := newServer(t)
		s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
		s.see("Message Pepper…")
		s.typeText(text)
		s.see(text)
		if strings.Contains(s.text(), "/status") {
			t.Fatalf("%q opened the menu:\n%s", text, s.text())
		}
	}
}

func TestTheSlashMenuKeys(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/")
	s.see("❯ /help")
	s.key(tcell.KeyDown)
	s.see("❯ /open")
	s.key(tcell.KeyUp)
	s.key(tcell.KeyUp) // round to the end
	s.see("❯ /exit")
	s.key(tcell.KeyDown) // and back
	s.see("❯ /help")
	s.key(tcell.KeyDown)
	s.key(tcell.KeyDown) // /ponies
	s.see("❯ /ponies")
	s.key(tcell.KeyTab)
	if got := s.row("> /ponies"); !strings.Contains(got, "> /ponies") {
		t.Fatalf("%q", got)
	}
	s.key(tcell.KeyEnter)
	s.see("Which pony")
}

func TestEnterRunsWhatsPicked(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/st")
	s.see("❯ /status")
	s.key(tcell.KeyEnter)
	s.see("pepper is available")
	var n int
	f.with(func() { n = len(f.said) })
	if n != 0 {
		t.Fatal("a command reached the pony")
	}
}

func TestEscPutsTheMenuAwayBeforeItLeaves(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("Message Pepper…")
	s.typeText("/")
	s.see("❯ /help")
	s.key(tcell.KeyEscape)
	s.gone("❯ /help")
	select {
	case <-s.done:
		t.Fatal("Esc left when it should have closed the menu")
	case <-time.After(30 * time.Millisecond):
	}
	// It comes back when the text changes.
	s.typeText("h")
	s.see("❯ /help")
}

func TestTheTopWordFollowsThePony(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("<pepper@mail.test>  resting")

	// While it works on this chat, it says so at once.
	s.typeText("do something")
	s.key(tcell.KeyEnter)
	s.see("<pepper@mail.test>  working")
	s.gone("<pepper@mail.test>  resting")
	card := s.waitCard(f)

	// Its mood when it's done, read from the pony.
	f.with(func() { f.ponies[0].Mood = "satisfied" })
	f.answer(card, "done")
	s.see("<pepper@mail.test>  satisfied")

	// And it changes with nothing else going on.
	f.with(func() { f.ponies[0].Mood = "curious" })
	s.see("<pepper@mail.test>  curious")

	// Working on another card shows too.
	f.with(func() { f.ponies[0].Status = "working" })
	s.see("<pepper@mail.test>  working")
}

func TestTheTopWordKeepsItsLastWhenItCantRead(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("<pepper@mail.test>  resting")
	f.with(func() { f.down = true })
	time.Sleep(60 * time.Millisecond)
	s.see("<pepper@mail.test>  resting")
}

func TestTheTopWordInANewChat(t *testing.T) {
	f := newServer(t)
	s := sit(t, f, Options{Pony: "pepper"}, 80, 24)
	s.see("<pepper@mail.test>  resting")
	f.with(func() { f.ponies[0].Mood = "focused" })
	s.see("<pepper@mail.test>  focused") // a chat with no card yet still follows the pony
}

// Your lines' dash has a colour of its own; the words are grey.
func TestYourLinesHaveAColouredDash(t *testing.T) {
	f := newServer(t)
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: []api.Line{{Seq: 2, From: "you", Text: "a question"}}}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 80, 24)
	s.see("- a question")
	var dash, word *tcell.SimCell
	cells := s.cells()
	for i := range cells {
		if len(cells[i].Bytes) == 0 {
			continue
		}
		if cells[i].Bytes[0] == '-' && dash == nil {
			dash = &cells[i]
		}
		if cells[i].Bytes[0] == 'q' && word == nil {
			word = &cells[i]
		}
	}
	if dash == nil || word == nil {
		t.Fatal("not on the screen")
	}
	if fg, _, _ := dash.Style.Decompose(); fg == tcell.ColorDefault {
		t.Fatal("the dash has no colour")
	}
	if _, _, attr := word.Style.Decompose(); attr&tcell.AttrDim == 0 {
		t.Fatal("your words should be grey")
	}
}

// Repainting from the screen's own goroutine mustn't wedge it, however much
// comes at once.
func TestALongConversationOpens(t *testing.T) {
	f := newServer(t)
	lines := make([]api.Line, 0, 400)
	for i := range 400 {
		lines = append(lines, api.Line{Seq: i + 2, From: "pony", Text: "line " + strconv.Itoa(i)})
	}
	f.cards["wch_a"] = &fakeCard{pony: "pepper", state: "idle", lines: lines}
	prefs := prefsIn(t)
	prefs.Put(f.srv.URL, "ada@example.com", auth.Last{Pony: "pepper", Chats: map[string]string{"pepper": "wch_a"}})
	s := sit(t, f, Options{Prefs: prefs}, 80, 24)
	s.see("line 399")
	for range 5 {
		s.screen.SetSize(70, 24)
		s.screen.PostEventWait(tcell.NewEventResize(70, 24))
		s.screen.SetSize(80, 24)
		s.screen.PostEventWait(tcell.NewEventResize(80, 24))
	}
	s.typeText("/clear")
	s.key(tcell.KeyEnter)
	s.gone("line 399")
}

func aBoard() []api.Lane {
	return []api.Lane{
		{ID: "backlog", Name: "Backlog", Count: 0},
		{ID: "ready", Name: "Ready", Count: 1, Cards: []api.BoardCard{{ID: "c1", Title: "Compare quotes", Pony: "pepper"}}},
		{ID: "doing", Name: "In progress", Count: 1, Cards: []api.BoardCard{{ID: "c2", Title: "Fix the site", Pony: "juniper", Badge: "needs_you"}}},
		{ID: "blocked", Name: "Blocked", Count: 0},
		{ID: "done", Name: "Done", Count: 120, Cards: []api.BoardCard{{ID: "c3", Title: "Paid the bill", Pony: "pepper"}}},
	}
}

func TestTheBoard(t *testing.T) {
	f := newServer(t)
	f.lanes = aBoard()
	s := sit(t, f, Options{Pony: "pepper"}, 120, 30)
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Compare quotes")
	s.see("Ready 1")
	s.see("In progress 1")
	s.see("Fix the site")
	s.see("juniper  needs you")
	s.see("Done 120")
	s.see("+ 119 more")
	s.see("live")

	// Lanes stand side by side.
	if got := s.row("Ready 1"); !strings.Contains(got, "In progress 1") || !strings.Contains(got, "Done 120") {
		t.Fatalf("%q", got)
	}
	// It stays live.
	f.with(func() {
		f.lanes[1].Cards = append(f.lanes[1].Cards, api.BoardCard{ID: "c9", Title: "A new card", Pony: "juniper"})
		f.lanes[1].Count = 2
	})
	s.see("A new card")
	s.see("Ready 2")

	// Esc goes back, and it stops reading.
	s.key(tcell.KeyEscape)
	s.see("Message Pepper…")
	time.Sleep(30 * time.Millisecond)
	var before, after int
	f.with(func() { before = f.boards })
	time.Sleep(60 * time.Millisecond)
	f.with(func() { after = f.boards })
	if after != before {
		t.Fatalf("still reading the board: %d then %d", before, after)
	}
}

func TestTheBoardCarriesOnThroughAFailure(t *testing.T) {
	f := newServer(t)
	f.lanes = aBoard()
	s := sit(t, f, Options{Pony: "pepper"}, 120, 30)
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Compare quotes")
	f.with(func() { f.down = true })
	s.see("Reconnecting…")
	s.see("Compare quotes") // what it knew stays
	f.with(func() { f.down = false })
	s.see("live")
}

func TestAnOlderServerHasNoBoard(t *testing.T) {
	f := newServer(t)
	f.noBoard = true
	s := sit(t, f, Options{Pony: "pepper"}, 100, 30)
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Couldn't read the board")
	s.see("Message Pepper…")
}

func TestLeavingFromTheBoard(t *testing.T) {
	f := newServer(t)
	f.lanes = aBoard()
	s := sit(t, f, Options{Pony: "pepper"}, 120, 30)
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Compare quotes")
	s.key(tcell.KeyCtrlC)
	s.left()
}

func TestOpeningFromTheBoard(t *testing.T) {
	f := newServer(t)
	f.lanes = aBoard()
	opened := make(chan string, 1)
	s := sit(t, f, Options{Pony: "pepper", Open: func(u string) error { opened <- u; return nil }}, 120, 30)
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Compare quotes")
	s.key(tcell.KeyCtrlO)
	select {
	case got := <-opened:
		if got != f.srv.URL+"/board" {
			t.Fatalf("%q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing opened")
	}
}

func TestRenderBoardColumns(t *testing.T) {
	out := renderBoard(aBoard(), 110, 30, true)
	rows := strings.Split(out, "\n")
	if !strings.Contains(rows[0], "Backlog 0") || !strings.Contains(rows[0], "Done 120") {
		t.Fatalf("%q", rows[0])
	}
	if at := strings.Index(rows[0], "Ready"); at < 15 {
		t.Fatalf("lanes should stand apart: %q", rows[0])
	}
}

func TestRenderBoardCutsToTheRoomThereIs(t *testing.T) {
	lanes := aBoard()
	for i := range 40 {
		lanes[1].Cards = append(lanes[1].Cards, api.BoardCard{ID: strconv.Itoa(i), Title: "Card " + strconv.Itoa(i), Pony: "pepper"})
	}
	lanes[1].Count = len(lanes[1].Cards)
	out := renderBoard(lanes, 110, 20, true)
	if n := strings.Count(out, "\n"); n > 20 {
		t.Fatalf("%d rows for 20:\n%s", n, out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("it should say what it left out:\n%s", out)
	}
}

func TestRenderBoardTruncatesWideTitles(t *testing.T) {
	lanes := aBoard()
	lanes[1].Cards[0].Title = strings.Repeat("a very long title ", 10)
	out := renderBoard(lanes, 100, 30, true)
	for _, l := range strings.Split(out, "\n") {
		if tview.TaggedStringWidth(l) > 100 {
			t.Fatalf("%d wide: %q", tview.TaggedStringWidth(l), l)
		}
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("it should show it was cut:\n%s", out)
	}
}

func TestRenderBoardStacksWhenNarrow(t *testing.T) {
	out := renderBoard(aBoard(), 40, 30, true)
	var ready, doing int
	for i, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Ready 1") {
			ready = i
		}
		if strings.Contains(l, "In progress 1") {
			doing = i
		}
	}
	if doing <= ready {
		t.Fatalf("lanes should stack:\n%s", out)
	}
	if !strings.Contains(out, "Compare quotes") || !strings.Contains(out, "+ 119 more") {
		t.Fatalf("%s", out)
	}
}

func TestRenderBoardLeavesEmptyLanesOutToFit(t *testing.T) {
	out := renderBoard(aBoard(), 76, 24, true) // an 80 column terminal
	first := strings.SplitN(out, "\n", 2)[0]
	if strings.Contains(first, "Backlog") || strings.Contains(first, "Blocked") {
		t.Fatalf("%q", first)
	}
	if !strings.Contains(first, "Ready 1") || !strings.Contains(first, "In progress 1") || !strings.Contains(first, "Done 120") {
		t.Fatalf("%q", first)
	}
}

func TestRenderBoardWordsBadges(t *testing.T) {
	lanes := []api.Lane{{Name: "Ready", Count: 4, Cards: []api.BoardCard{
		{Title: "a", Pony: "pepper", Badge: "asked"},
		{Title: "b", Pony: "pepper", Badge: "stopped"},
		{Title: "c", Badge: "waiting"},
		{Title: "d", Pony: "a-very-long-pony-name-indeed", Badge: "needs_you"},
	}}}
	out := regexp.MustCompile(`\[[^\]]*\]`).ReplaceAllString(renderBoard(lanes, 20, 30, true), "")
	for _, want := range []string{"pepper  asked you", "pepper  stopped", "waiting", "a-very-l…  needs you"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
}

func TestRenderBoardWithNothingOnIt(t *testing.T) {
	lanes := []api.Lane{{Name: "Ready"}, {Name: "Done"}, {Name: "Blocked"}}
	if out := renderBoard(lanes, 30, 24, true); !strings.Contains(out, "Ready 0") {
		t.Fatalf("%q", out)
	}
}

func TestTheAddressIsReadableFromTheBoard(t *testing.T) {
	f := newServer(t)
	f.lanes = aBoard()
	s := sit(t, f, Options{Pony: "pepper"}, 120, 30) // no browser here
	s.see("Message Pepper…")
	s.typeText("/board")
	s.key(tcell.KeyEnter)
	s.see("Compare quotes")
	s.key(tcell.KeyCtrlO)
	s.see("Open this in a browser:")
	s.see("/board")
	s.see("Message Pepper…")
}

func TestRenderBoardIsInert(t *testing.T) {
	lanes := aBoard()
	lanes[1].Cards[0].Title = "[red]not red[-]"
	s := renderBoard(lanes, 110, 30, false)
	if strings.Contains(s, "[red]not") {
		t.Fatalf("a title read as markup: %q", s)
	}
}
