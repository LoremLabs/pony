package cli

import (
	"context"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

// logServer answers the log routes as OnePony does. Each read takes the next
// of its pages (the last repeats), and it keeps what it was asked.
type logServer struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	pages []api.Log
	reads int
	paths []string
	fail  map[int]int // read number → status
}

func newLogServer(t *testing.T, pages ...api.Log) *logServer {
	ls := &logServer{t: t, pages: pages, fail: map[int]int{}}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		ls.paths = append(ls.paths, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/ponies/stranger/") {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "no_such_pony", "message": "no such pony"}})
			return
		}
		i := ls.reads
		ls.reads++
		if status := ls.fail[i]; status != 0 {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "unavailable", "message": "unavailable"}})
			return
		}
		if i >= len(ls.pages) {
			i = len(ls.pages) - 1
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "events": ls.pages[i].Events, "next": ls.pages[i].Next})
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

func (ls *logServer) asked() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]string{}, ls.paths...)
}

func ev(id, at, who, text string) api.Event {
	return api.Event{ID: id, At: at, Pony: "pepper", Who: who, Kind: "said", Text: text}
}

const today = "2026-11-05T14:30:00.000000Z"

func TestLogTime(t *testing.T) {
	// This machine's zone doesn't come into it.
	old := time.Local
	time.Local = time.FixedZone("Paris", 3600)
	t.Cleanup(func() { time.Local = old })
	cases := map[string]string{
		"2026-11-05T14:30:01.123456Z": "2026-11-05T14:30:01Z",
		"2026-11-04T09:05:00Z":        "2026-11-04T09:05:00Z", // another day reads the same way
		"2026-11-05T15:30:00+01:00":   "2026-11-05T14:30:00Z",
		"not a time":                  "not a time",
	}
	for at, want := range cases {
		if got := logTime(at); got != want {
			t.Errorf("%s: got %q, want %q", at, got, want)
		}
	}
}

func TestPonyColour(t *testing.T) {
	// crc32(name) % colours, so a pony's colour is the same on every machine.
	// A change to the list recolours every pony: these say so.
	for pony, want := range map[string]string{
		"pepper":  ponyColours[crc32.ChecksumIEEE([]byte("pepper"))%14],
		"juniper": ponyColours[crc32.ChecksumIEEE([]byte("juniper"))%14],
	} {
		if got := ponyColour(pony); got != want {
			t.Errorf("%s: got %q, want %q", pony, got, want)
		}
	}
	seen := map[string]bool{}
	for _, p := range []string{"pepper", "juniper", "moss", "fern", "sophie", "emmy", "barley", "clover", "rye"} {
		seen[ponyColour(p)] = true
	}
	if len(seen) < 4 {
		t.Fatalf("too few colours for nine ponies: %v", seen)
	}
	half := len(ponyColours) / 2
	for i, c := range ponyColours[:half] {
		if ponyColours[half+i] != "1;"+c {
			t.Fatalf("%q has no bold twin", c)
		}
	}
	// A name never takes a colour that says how a line went.
	for _, c := range ponyColours {
		for _, status := range []string{red, yellow, green, bold, dim} {
			if c == status {
				t.Fatalf("%q is a status colour", c)
			}
		}
	}
}

// aLogTerminal is a view on a terminal of cols columns, colour off unless
// asked for, so a test can read the layout.
func aLogTerminal(t *testing.T, cols int, env map[string]string) *logView {
	t.Helper()
	aTerminal(t)
	if env == nil {
		env = map[string]string{"NO_COLOR": "1"}
	}
	withEnv(t, env)
	termSize = func(io.Writer) (int, int) { return cols, 40 }
	return newLogView(&strings.Builder{}, false)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// a card's events, as the server words them.
func cardEvent(card, kind, text string) api.Event {
	return api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: kind, Card: card, Title: "Quotes", Text: text}
}

func TestLogPlainLineIsWhole(t *testing.T) {
	var b strings.Builder
	withEnv(t, map[string]string{})
	v := newLogView(&b, false)
	e := api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "done", Card: "c1", Title: "Quotes", Text: "answered · Quotes", Run: "run_a", RID: "Cg1"}
	for range 2 { // the same card twice: still a whole record, every time
		if got, want := v.line(e), "2026-11-05T14:30:00Z  pepper answered · Quotes"; got != want {
			t.Errorf("plain: %q", got)
		}
	}
	v.debug = true
	if got, want := v.line(e), "2026-11-05T14:30:00Z  pepper answered · Quotes  run=run_a rid=Cg1"; got != want {
		t.Errorf("debug: %q", got)
	}
	v.card, v.any = "other", true
	if got := v.line(e); strings.HasPrefix(got, "\n") {
		t.Errorf("no space between cards in a pipe: %q", got)
	}
}

func TestLogTitleOnce(t *testing.T) {
	v := aLogTerminal(t, 200, nil)
	say := func(e api.Event) string {
		return strings.TrimSpace(strings.SplitN(strings.TrimLeft(v.line(e), "\n"), "pepper", 2)[1])
	}
	for _, c := range []struct {
		e    api.Event
		want string
	}{
		{cardEvent("c1", "started", "started on “Quotes”"), "started on “Quotes”"},
		{cardEvent("c1", "said", "said: Got it."), "said: Got it."},
		{cardEvent("c1", "effect", "sent an email to a@b.test · Quotes"), "sent an email to a@b.test"},
		{cardEvent("c1", "moved", "moved “Quotes” to Done"), "moved it to Done"},
		{cardEvent("c1", "done", "answered · Quotes"), "✓ answered"},
		{cardEvent("c2", "done", "answered · Quotes"), "✓ answered · Quotes"}, // another card: says which
		{cardEvent("c2", "filed", "filed “Quotes” in Ready"), "filed it in Ready"},
		{cardEvent("c1", "started", "started on “Quotes”"), "started on “Quotes”"}, // back again
	} {
		if got := say(c.e); got != c.want {
			t.Errorf("%q: got %q, want %q", c.e.Text, got, c.want)
		}
	}
}

// A card that opens with what was said names itself on its first line that can.
func TestLogTitleWaitsForALineThatNamesIt(t *testing.T) {
	v := aLogTerminal(t, 200, nil)
	v.line(cardEvent("c1", "said", "said: hi"))
	if got := v.line(cardEvent("c1", "done", "answered · Quotes")); !strings.Contains(got, "answered · Quotes") {
		t.Fatalf("%q", got)
	}
}

// A chat's title is its first line, so saying that line has named the card.
func TestLogTitleAlreadySaid(t *testing.T) {
	v := aLogTerminal(t, 200, nil)
	title := "can you ping the host for 5 seconds and tell me the aver…"
	said := api.Event{At: today, Pony: "pepper", Who: "you", Kind: "message", Card: "c1", Title: title, Text: "said: can you ping the host for 5 seconds and tell me the average"}
	if got := v.line(said); !strings.Contains(got, "said: can you ping") {
		t.Fatalf("%q", got)
	}
	started := api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "started", Card: "c1", Title: title, Text: "started on “" + title + "”"}
	if got := v.line(started); !strings.HasSuffix(got, "started on it") {
		t.Fatalf("it should say it, not the line again: %q", got)
	}
	// A later line of the chat is no title, and the card still needs naming.
	w := aLogTerminal(t, 200, nil)
	other := said
	other.Text = "said: and another thing"
	w.line(other)
	if got := w.line(started); !strings.Contains(got, title) {
		t.Fatalf("%q", got)
	}
}

func TestLogColumnsLineUp(t *testing.T) {
	v := aLogTerminal(t, 200, nil)
	events := []api.Event{
		{At: today, Pony: "proud", Who: "you", Kind: "said", Text: "said: hi"},
		{At: today, Pony: "proud", Who: "proud", Kind: "done", Text: "answered"},
		{At: today, Pony: "proud", Who: "proud", Kind: "abandoned", Text: "could not finish"},
		{At: today, Pony: "proud", Who: "proud", Kind: "waiting", Text: "is waiting"},
	}
	v.measure(events)
	var at []int
	for _, e := range events {
		line := v.line(e)
		at = append(at, strings.Index(line, e.Text))
	}
	want := len("2026-11-05T14:30:00Z") + 2 + len("proud") + 2 + 4 // a mark is three bytes, and its space
	if at[1] != want || at[2] != want || at[3] != want {
		t.Fatalf("marked lines start at %v, want %d", at, want)
	}
	if at[0] != want-2 { // no mark, in a column of spaces
		t.Fatalf("unmarked line starts at %d", at[0])
	}
	// A name past the limit runs long, and pushes nothing else.
	if v.measure([]api.Event{{Who: strings.Repeat("x", 60)}}); v.names != mostName {
		t.Fatalf("names column %d", v.names)
	}
}

func TestLogFitsTheScreen(t *testing.T) {
	v := aLogTerminal(t, 60, nil)
	long := strings.Repeat("a word ", 30)
	for _, e := range []api.Event{
		{At: today, Pony: "pepper", Who: "pepper", Kind: "said", Card: "c1", Text: "said: " + long},
		{At: today, Pony: "pepper", Who: "pepper", Kind: "said", Card: "c2", Text: "said: " + strings.Repeat("日本語", 30)},
		{At: today, Pony: "pepper", Who: "pepper", Kind: "done", Card: "c3", Title: long, Text: "answered · " + long},
	} {
		line := strings.TrimLeft(v.line(e), "\n")
		if n := cellsOf(ansi.ReplaceAllString(line, "")); n > 59 {
			t.Fatalf("%d columns: %q", n, line)
		}
		if !strings.HasSuffix(line, "…") {
			t.Fatalf("no sign it was cut: %q", line)
		}
	}
	short := v.line(api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "said", Card: "c4", Text: "said: hi"})
	if strings.Contains(short, "…") {
		t.Fatalf("cut what fits: %q", short)
	}
}

func TestLogKeepsItsIDsWhenCut(t *testing.T) {
	v := aLogTerminal(t, 70, nil)
	v.debug = true
	line := v.line(api.Event{At: today, Pony: "p", Who: "p", Kind: "said", Text: "said: " + strings.Repeat("w ", 80), Run: "run_a", RID: "Cg1"})
	if !strings.HasSuffix(line, "run=run_a rid=Cg1") || cellsOf(line) > 69 {
		t.Fatalf("%q", line)
	}
}

// Too narrow to be worth cutting: it wraps, and says everything.
func TestLogNarrowTerminalDoesNotCutToNothing(t *testing.T) {
	v := aLogTerminal(t, 30, nil)
	if got := v.line(api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "said", Text: "said: all of it"}); !strings.Contains(got, "said: all of it") {
		t.Fatalf("%q", got)
	}
}

func TestLogSpaceBetweenCards(t *testing.T) {
	v := aLogTerminal(t, 200, nil)
	if got := v.line(cardEvent("c1", "said", "said: one")); strings.HasPrefix(got, "\n") {
		t.Fatalf("a space before the first line: %q", got)
	}
	if got := v.line(cardEvent("c1", "said", "said: two")); strings.HasPrefix(got, "\n") {
		t.Fatalf("a space inside a card: %q", got)
	}
	if got := v.line(cardEvent("c2", "said", "said: three")); !strings.HasPrefix(got, "\n") {
		t.Fatalf("no space between cards: %q", got)
	}
	// A pony's lines that aren't a card's are a group of their own.
	if got := v.line(api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "file_reading", Text: "is reading “a.pdf”"}); !strings.HasPrefix(got, "\n") {
		t.Fatalf("%q", got)
	}
}

func TestLogColours(t *testing.T) {
	v := aLogTerminal(t, 200, map[string]string{"TERM": "xterm-256color"})
	line := func(e api.Event) string { return strings.TrimLeft(v.line(e), "\n") }
	got := line(api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "done", Card: "c1", Title: "Quotes", Text: "answered · Quotes"})
	want := "\x1b[2m2026-11-05T14:30:00Z\x1b[0m  \x1b[" + ponyColour("pepper") + "mpepper\x1b[0m  \x1b[32m✓\x1b[0m \x1b[32manswered\x1b[0m\x1b[2m · Quotes\x1b[0m"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := line(api.Event{At: today, Pony: "pepper", Who: "you", Kind: "said", Text: "said: hi"}); !strings.Contains(got, "\x1b[1myou   \x1b[0m    said: hi") {
		t.Errorf("you should stand out, in the column: %q", got)
	}
	if got := line(api.Event{At: today, Pony: "p", Who: "p", Kind: "abandoned", Text: "gave up"}); !strings.Contains(got, "\x1b[31m✗\x1b[0m \x1b[31mgave up") {
		t.Errorf("trouble should be red: %q", got)
	}
	if got := line(api.Event{At: today, Pony: "p", Who: "p", Kind: "waiting", Text: "stopped"}); !strings.Contains(got, "\x1b[33m…\x1b[0m \x1b[33mstopped") {
		t.Errorf("waiting should be yellow: %q", got)
	}
	if got := line(api.Event{At: today, Pony: "pepper", Who: "pepper", Kind: "started", Text: "started"}); strings.Contains(got, "\x1b[2m\x1b[0m") {
		t.Errorf("an empty colour: %q", got)
	}
}

func TestLogViewOnlyOnATerminal(t *testing.T) {
	var b strings.Builder
	withEnv(t, map[string]string{})
	if v := newLogView(&b, false); v.pretty || v.colour {
		t.Fatal("a view on something that isn't a terminal")
	}
	aTerminal(t)
	if v := newLogView(&b, false); !v.pretty || !v.colour {
		t.Fatal("no view on a terminal")
	}
	withEnv(t, map[string]string{"NO_COLOR": "1"})
	if v := newLogView(&b, false); !v.pretty || v.colour {
		t.Fatal("NO_COLOR takes the colour and leaves the layout")
	}
	withEnv(t, map[string]string{"TERM": "dumb"})
	if v := newLogView(&b, false); v.pretty || v.colour {
		t.Fatal("a view on a dumb terminal")
	}
}

func TestFit(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly 10", 10, "exactly 10"},
		{"a little over", 10, "a little…"},
		{"日本語日本語", 7, "日本語…"},
	} {
		if got := fit(c.s, c.n); got != c.want {
			t.Errorf("%q: got %q, want %q", c.s, got, c.want)
		}
	}
}

// Following on a terminal keeps the view going from one read to the next.
func TestFollowOnATerminalKeepsItsPlace(t *testing.T) {
	quick(t)
	aTerminal(t)
	a, b := cardEvent("c1", "started", "started on “Quotes”"), cardEvent("c1", "done", "answered · Quotes")
	a.ID, b.ID = "a", "b"
	s := newLogServer(t, api.Log{Events: []api.Event{a}, Next: "t1"}, api.Log{Events: []api.Event{a, b}, Next: "t2"}, api.Log{Next: "t2"})
	s.fail[2] = 401
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL, "NO_COLOR": "1"})
	_, out, _ := run("log", "-f", "pepper")
	if !strings.Contains(out, "started on “Quotes”") || !strings.Contains(out, "✓ answered\n") {
		t.Fatalf("the title should show once across reads: %q", out)
	}
}

func TestLogPipedHasNoEscapes(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{{ID: "a", At: today, Pony: "pepper", Who: "pepper", Kind: "done", Text: "answered"}}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	if code, out, _ := run("log", "pepper"); code != ExitOK || strings.Contains(out, "\x1b") {
		t.Fatalf("exit %d, %q", code, out)
	}
}

func TestLogDebugFlag(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{{ID: "a", At: today, Pony: "pepper", Who: "pepper", Kind: "said", Text: "said: hi", Run: "run_a", RID: "Cg1"}}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	if _, out, _ := run("log", "pepper"); strings.Contains(out, "Cg1") {
		t.Fatalf("ids without --debug: %q", out)
	}
	if _, out, _ := run("log", "--debug", "pepper"); !strings.Contains(out, "run=run_a rid=Cg1") {
		t.Fatalf("no ids under --debug: %q", out)
	}
}

func TestLog(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{
		ev("card:c1:1", today, "you", "filed “Quotes” in Ready"),
		ev("card:c1:2", "2026-11-05T14:30:05.000000Z", "pepper", "started on “Quotes”"),
	}, Next: "2026-11-05T14:30:05.000000Z"})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, errOut := run("log", "pepper", "-n", "5")
	want := "2026-11-05T14:30:00Z  you filed “Quotes” in Ready\n2026-11-05T14:30:05Z  pepper started on “Quotes”\n"
	if code != ExitOK || out != want {
		t.Fatalf("exit %d, %q (stderr %q)", code, out, errOut)
	}
	if got := s.asked()[0]; got != "/api/cli/v1/ponies/pepper/log?limit=5" {
		t.Fatalf("asked %q", got)
	}
	noToken(t, out, errOut)
}

func TestLogEveryPony(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "pepper", "said: hi")}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	if code, _, errOut := run("log"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := s.asked()[0]; got != "/api/cli/v1/log?limit="+strconv.Itoa(logDefault) {
		t.Fatalf("asked %q", got)
	}
}

func TestLogJSON(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "pepper", "said: hi")}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, _ := run("log", "--json")
	var got []api.Event
	if code != ExitOK || json.Unmarshal([]byte(out), &got) != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("exit %d, %q", code, out)
	}

	empty := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": empty.srv.URL})
	if code, out, _ := run("log", "--json"); code != ExitOK || strings.TrimSpace(out) != "[]" {
		t.Fatalf("no lines should be an empty list: exit %d, %q", code, out)
	}
}

func TestLogNothingYet(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, errOut := run("log", "pepper")
	if code != ExitOK || out != "" || !strings.Contains(errOut, "nothing to show yet") {
		t.Fatalf("exit %d, %q, %q", code, out, errOut)
	}
}

func TestLogNoSuchPony(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, _, errOut := run("log", "stranger")
	if code != ExitNotFound || !strings.Contains(errOut, `no pony "stranger"`) {
		t.Fatalf("exit %d: %q", code, errOut)
	}
}

func TestLogFlags(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	for _, n := range []string{"0", "101", "-3"} {
		if code, _, _ := run("log", "-n", n); code != ExitUsage {
			t.Fatalf("-n %s: exit %d", n, code)
		}
	}
	if code, _, _ := run("log", "pepper", "juniper"); code != ExitUsage {
		t.Fatalf("two ponies: exit %d", code)
	}
}

// follow runs followLog from the first page until it has read n more times.
func follow(t *testing.T, s *logServer, stop int, args ...string) (lines []string, code int) {
	t.Helper()
	quick(t)
	c, err := api.New(s.srv.URL, testToken, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := c.ReadLog(ctx, "pepper", "", logDefault)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range 5000 {
			s.mu.Lock()
			done := s.reads >= stop
			s.mu.Unlock()
			if done {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	var mu sync.Mutex
	code = followLog(ctx, c, "pepper", first, func(e api.Event) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, e.ID)
	}, &strings.Builder{})
	return lines, code
}

func TestFollowPrintsEachLineOnce(t *testing.T) {
	a, b, c, d := ev("a", today, "p", "1"), ev("b", today, "p", "2"), ev("c", today, "p", "3"), ev("d", today, "p", "4")
	s := newLogServer(t,
		api.Log{Events: []api.Event{a, b}, Next: "t1"},
		api.Log{Events: []api.Event{b}, Next: "t1"},    // what's near the cursor comes again
		api.Log{Events: []api.Event{b, c}, Next: "t2"}, // a line arrives
		api.Log{Events: nil, Next: "t2"},               // nothing, and nothing forgotten
		api.Log{Events: []api.Event{c, d}, Next: "t3"}, // the line before it again, and a new one
		api.Log{Events: []api.Event{c, d}, Next: "t3"},
	)
	lines, code := follow(t, s, 8)
	if code != 130 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.Join(lines, ""); got != "cd" {
		t.Fatalf("printed %q, want each new line once", got)
	}
	paths := s.asked()
	if !strings.Contains(paths[len(paths)-1], "after=t3") {
		t.Fatalf("it should read on from the cursor: %v", paths)
	}
}

// The lines before the ones shown must not come back as new ones.
func TestFollowRemembersMoreThanItShows(t *testing.T) {
	quick(t)
	older, newest, fresh := ev("older", today, "p", "older"), ev("newest", today, "p", "newest"), ev("fresh", today, "p", "fresh")
	s := newLogServer(t,
		api.Log{Events: []api.Event{older, newest}, Next: "t1"},
		api.Log{Events: []api.Event{older, newest, fresh}, Next: "t2"}, // what's near the cursor comes again
		api.Log{Next: "t2"},
	)
	s.fail[2] = 401 // ends the follow
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, _ := run("log", "-f", "-n", "1", "pepper")
	if code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
	if got := strings.ReplaceAll(out, "2026-11-05T14:30:00Z  p ", ""); got != "newest\nfresh\n" {
		t.Fatalf("printed %q", got)
	}
	if got := s.asked()[0]; !strings.Contains(got, "limit="+strconv.Itoa(logMost)) {
		t.Fatalf("following should read all it can first: %q", got)
	}
}

func TestFollowKeepsTheCursorThroughAnEmptyRead(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "p", "1")}, Next: "t1"}, api.Log{})
	follow(t, s, 4)
	paths := s.asked()
	if !strings.Contains(paths[len(paths)-1], "after=t1") {
		t.Fatalf("the cursor was lost: %v", paths)
	}
}

func TestFollowRidesOutAFailure(t *testing.T) {
	x := ev("x", today, "p", "1")
	s := newLogServer(t, api.Log{Next: "t1"}, api.Log{Next: "t1"}, api.Log{Next: "t1"}, api.Log{Events: []api.Event{x}, Next: "t2"})
	s.fail[1], s.fail[2] = 503, 502
	lines, code := follow(t, s, 6)
	if code != 130 || strings.Join(lines, "") != "x" {
		t.Fatalf("exit %d, %v", code, lines)
	}
}

func TestFollowStopsWhenSignedOut(t *testing.T) {
	s := newLogServer(t, api.Log{Next: "t1"})
	s.fail[1] = 401
	if _, code := follow(t, s, 50); code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
}

func TestFollowGivesUpOnADeadServer(t *testing.T) {
	s := newLogServer(t, api.Log{Next: "t1"})
	for i := 1; i < 20; i++ {
		s.fail[i] = 503
	}
	if _, code := follow(t, s, 50); code == 130 || code == ExitOK {
		t.Fatalf("exit %d: it should have given up", code)
	}
}

func TestHelpNamesLog(t *testing.T) {
	code, out, _ := run("help")
	if code != ExitOK || !strings.Contains(out, "log") {
		t.Fatalf("exit %d, %q", code, out)
	}
}
