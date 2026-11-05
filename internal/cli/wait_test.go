package cli

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/loremlabs/pony/internal/api"
)

// aTerminal makes every writer a terminal of 100 by 40, drawing quickly.
func aTerminal(t *testing.T) {
	t.Helper()
	oldTerm, oldEvery, oldSize := isTerminal, waitEvery, termSize
	isTerminal, waitEvery = func(io.Writer) bool { return true }, time.Millisecond
	termSize = func(io.Writer) (int, int) { return 100, 40 }
	t.Cleanup(func() { isTerminal, waitEvery, termSize = oldTerm, oldEvery, oldSize })
}

// aPonyTerminal is an environment that can draw the pony.
var aPonyTerminal = map[string]string{"TERM": "xterm-256color"}

func with(env map[string]string, more map[string]string) map[string]string {
	all := map[string]string{}
	for _, m := range []map[string]string{env, more} {
		for k, v := range m {
			all[k] = v
		}
	}
	return all
}

// cursorUp is the end of every frame: back up to the top of the drawing.
var cursorUp = fmt.Sprintf("\x1b[%dA", waitRows)

func TestPonyFrames(t *testing.T) {
	for f, frame := range ponyFrames {
		eye := false
		for y, row := range frame {
			if len(row) != ponyWidth {
				t.Fatalf("frame %d row %d is %d wide", f, y, len(row))
			}
			for x := range len(row) {
				if _, ok := ponyInks[row[x]]; !ok && row[x] != '.' {
					t.Fatalf("frame %d row %d: no ink for %q", f, y, row[x])
				}
			}
			eye = eye || strings.Contains(row, "k")
		}
		if !eye {
			t.Errorf("frame %d has no eye", f)
		}
	}
	if ponyFrames[0] == ponyFrames[walkFrames/2] {
		t.Fatal("the frames of a walk should differ")
	}
	if ponyFrames[grazing] == ponyFrames[chewing] {
		t.Fatal("chewing should show")
	}
	if ponyFrames[tailOneWay] == ponyFrames[tailOtherWay] {
		t.Fatal("the tail should swish")
	}
}

func TestPonyRow(t *testing.T) {
	cases := []struct{ name, row, want string }{
		{"nothing", "..", "  \x1b[0m"},
		{"one colour", "gg", "\x1b[48;5;244m  \x1b[0m"},
		{"two colours", "gc", "\x1b[48;5;244m \x1b[48;5;237m \x1b[0m"},
		{"back to nothing", "k.w", "\x1b[48;5;16m \x1b[0m \x1b[48;5;231m \x1b[0m"},
	}
	for _, c := range cases {
		if got := ponyRow(c.row); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// decodeRow reads a drawn row back as a terminal would: a colour number for
// each cell, 0 for the terminal's own background.
func decodeRow(t *testing.T, row string) (cells []int) {
	t.Helper()
	bg := 0
	for i := 0; i < len(row); {
		if strings.HasPrefix(row[i:], "\x1b[") {
			end := i + strings.IndexByte(row[i:], 'm')
			switch code := row[i+2 : end]; {
			case code == "0":
				bg = 0
			case strings.HasPrefix(code, "48;5;"):
				n, err := strconv.Atoi(code[len("48;5;"):])
				if err != nil {
					t.Fatalf("bad colour in %q", code)
				}
				bg = n
			default:
				t.Fatalf("unexpected code %q", code)
			}
			i = end + 1
			continue
		}
		if row[i] != ' ' {
			t.Fatalf("a glyph in the drawing: %q", row[i:])
		}
		cells = append(cells, bg)
		i++
	}
	return cells
}

// Every frame, both ways, comes back as the drawing it was made from, and
// it's nothing but coloured spaces.
func TestDrawingIsThePixels(t *testing.T) {
	d := ponyDrawing()
	for face, frames := range d {
		for f, rows := range frames {
			for y, row := range rows {
				want := ponyFrames[f][y]
				if face == 1 {
					want = mirrored(want)
				}
				got := decodeRow(t, row)
				if len(got) != ponyWidth {
					t.Fatalf("frame %d row %d is %d wide", f, y, len(got))
				}
				for x, ink := range got {
					if ink != ponyInks[want[x]] {
						t.Fatalf("face %d frame %d row %d x %d: got %d, want %d", face, f, y, x, ink, ponyInks[want[x]])
					}
				}
			}
		}
	}
}

func TestMirrored(t *testing.T) {
	if got := mirrored("abc"); got != "cba" {
		t.Fatal(got)
	}
}

// lowest picks the least of what it may, so a test's walks are its shortest.
func lowest(lo, _ int) int { return lo }

func TestAmbleWalksThenGrazes(t *testing.T) {
	a := amble{between: lowest, to: 20}
	walked, grazed := 0, 0
	last := 0.0
	for i := range 400 {
		x, left, frame := a.next(40)
		if x < 0 || x > 40 {
			t.Fatalf("frame %d walked off the stage: %d", i, x)
		}
		if frame < walkFrames {
			walked++
			if d := a.x - last; d > stride+0.001 || d < -stride-0.001 {
				t.Fatalf("frame %d moved %.2f columns", i, d)
			}
			if left != (a.x < last) {
				t.Fatalf("frame %d walked backwards", i)
			}
		} else {
			grazed++
			if a.x != last {
				t.Fatalf("frame %d grazing on the move", i)
			}
		}
		last = a.x
	}
	if walked == 0 || grazed == 0 {
		t.Fatalf("walked %d, grazed %d", walked, grazed)
	}
}

func TestAmbleTurnsAtTheEdges(t *testing.T) {
	seen := map[bool]bool{}
	n := 0
	// Always asked to go right: at the edge it comes back instead.
	a := amble{between: func(lo, hi int) int {
		n++
		if hi == 1 {
			return 0
		}
		return hi
	}, to: 40}
	for range 2000 {
		x, left, _ := a.next(40)
		if x < 0 || x > 40 {
			t.Fatalf("off the stage: %d", x)
		}
		seen[left] = true
	}
	if !seen[true] || !seen[false] {
		t.Fatalf("it should face both ways: %v", seen)
	}
}

func TestAmbleNoRoom(t *testing.T) {
	a := amble{between: lowest}
	for range 100 {
		if x, _, frame := a.next(0); x != 0 || frame < walkFrames {
			t.Fatalf("with nowhere to go it should stay and graze: %d, frame %d", x, frame)
		}
	}
}

func TestAmbleNarrowedTerminal(t *testing.T) {
	a := amble{between: lowest, x: 40, to: 44}
	if x, _, _ := a.next(10); x > 10 {
		t.Fatalf("stranded off the stage at %d", x)
	}
}

func TestGrazeFrames(t *testing.T) {
	seen := map[int]bool{}
	for beat := range 100 {
		f := grazeFrame(beat)
		if f < walkFrames || f >= ponyFrameCount {
			t.Fatalf("beat %d: frame %d", beat, f)
		}
		seen[f] = true
	}
	if len(seen) != ponyFrameCount-walkFrames {
		t.Fatalf("not every grazing drawing shows: %v", seen)
	}
}

func TestCanDrawPony(t *testing.T) {
	good := map[string]string{"TERM": "xterm-256color"}
	cases := []struct {
		name string
		goos string
		env  map[string]string
		cols int
		rows int
		want bool
	}{
		{"a usual terminal", "linux", good, 100, 40, true},
		{"a Mac", "darwin", good, 100, 40, true},
		{"just fits", "linux", good, minCols, minRows, true},
		{"too narrow", "linux", good, minCols - 1, 40, false},
		{"too short", "linux", good, 100, minRows - 1, false},
		{"no size known", "linux", good, 0, 0, false},
		{"Windows", "windows", good, 100, 40, false},
		{"NO_COLOR", "linux", with(good, map[string]string{"NO_COLOR": "1"}), 100, 40, false},
		{"the Linux console", "linux", map[string]string{"TERM": "linux"}, 100, 40, false},
		{"no 256 colours", "linux", map[string]string{"TERM": "xterm"}, 100, 40, false},
		{"COLORTERM says so", "linux", map[string]string{"TERM": "xterm", "COLORTERM": "truecolor"}, 100, 40, true},
		{"kitty", "linux", map[string]string{"TERM": "xterm-kitty"}, 100, 40, true},
	}
	for _, c := range cases {
		if got := canDrawPony(c.goos, func(k string) string { return c.env[k] }, c.cols, c.rows); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBusyWordsAndElapsed(t *testing.T) {
	if got := busyWords("pepper", "working"); got != "pepper is working" {
		t.Fatalf("%q", got)
	}
	if got := busyWords("pepper", "queued"); got != "pepper will get to this next" {
		t.Fatalf("%q", got)
	}
	if got := busyWords("pepper", "idle"); got != "" {
		t.Fatalf("%q", got)
	}
	if got := busyWords(strings.Repeat("é", 40), "working"); utf8.RuneCountInString(got) != nameCells+len(" is working") {
		t.Fatalf("a long name is cut: %q", got)
	}
	if got := elapsed(9 * time.Second); got != "9s" {
		t.Fatalf("%q", got)
	}
	if got := elapsed(125 * time.Second); got != "2m05s" {
		t.Fatalf("%q", got)
	}
}

// lockedBuffer can be read while a waiter writes to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestWaiterDrawsThePonyAndCleansUp(t *testing.T) {
	aTerminal(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows gets the spinner")
	}
	withEnv(t, aPonyTerminal)
	var out lockedBuffer
	w := startWaiting(&out, false, "sending to pepper")
	if w == nil || !strings.HasPrefix(out.String(), "\x1b[?25l") {
		t.Fatalf("the cursor should go first: %q", out.String())
	}
	w.say("pepper is working")
	w.say("") // a state with no words keeps the line
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "pepper is working") {
		if time.Now().After(deadline) {
			t.Fatalf("never drew the words: %q", out.String())
		}
		time.Sleep(time.Millisecond)
	}
	// Every frame ends back at the top of the drawing.
	marked := ""
	w.print(func() { marked = out.String(); out.Write([]byte("<answer>\n")) })
	if !strings.HasSuffix(marked, cursorUp+"\x1b[J") {
		t.Fatalf("the drawing wasn't cleared before the answer: %q", marked[max(0, len(marked)-40):])
	}
	after := func() string { s := out.String(); return s[strings.Index(s, "<answer>"):] }
	for !strings.Contains(after(), "\x1b[48;5;") {
		if time.Now().After(deadline) {
			t.Fatal("the pony never came back after the answer")
		}
		time.Sleep(time.Millisecond)
	}
	w.stop()
	w.stop() // twice is fine
	if got := out.String(); !strings.HasSuffix(got, "\x1b[J\x1b[?25h") {
		t.Fatalf("stop left the drawing, or the cursor hidden: %q", got[max(0, len(got)-40):])
	}
	if n := strings.Count(out.String(), "\x1b[?25h"); n != 1 {
		t.Fatalf("the cursor came back %d times", n)
	}
}

func TestWaiterShrinkingTerminal(t *testing.T) {
	aTerminal(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows gets the spinner")
	}
	withEnv(t, aPonyTerminal)
	var size atomic.Int32
	size.Store(100)
	termSize = func(io.Writer) (int, int) { return int(size.Load()), 40 }
	var out lockedBuffer
	w := startWaiting(&out, false, "waiting")
	for !strings.Contains(out.String(), "\x1b[48;5;") {
		time.Sleep(time.Millisecond)
	}
	size.Store(20)
	for !strings.Contains(out.String(), "\x1b[?25h") {
		time.Sleep(time.Millisecond)
	}
	for !strings.Contains(out.String()[strings.Index(out.String(), "\x1b[?25h"):], "waiting") {
		time.Sleep(time.Millisecond)
	}
	w.stop()
	if got := out.String(); strings.Count(got, "\x1b[?25h") != 1 || !strings.HasSuffix(got, "\r") {
		t.Fatalf("it should end as a spinner, cursor back once: %q", got[max(0, len(got)-40):])
	}
}

func TestWaiterPlainSpinner(t *testing.T) {
	aTerminal(t)
	withEnv(t, map[string]string{"TERM": "xterm"})
	var out lockedBuffer
	w := startWaiting(&out, false, "waiting")
	for !strings.Contains(out.String(), "waiting") {
		time.Sleep(time.Millisecond)
	}
	w.stop()
	got := out.String()
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x1b[48;5;") {
		t.Fatalf("escape codes where they may not work: %q", got)
	}
	if !strings.HasSuffix(got, "\r") {
		t.Fatalf("the line wasn't cleared: %q", got[max(0, len(got)-30):])
	}
}

func TestSpinnerFitsANarrowTerminal(t *testing.T) {
	aTerminal(t)
	withEnv(t, map[string]string{"TERM": "xterm"})
	termSize = func(io.Writer) (int, int) { return 12, 40 }
	var out lockedBuffer
	w := startWaiting(&out, false, "a line far wider than the terminal")
	for !strings.Contains(out.String(), "a line") {
		time.Sleep(time.Millisecond)
	}
	w.stop()
	for _, line := range strings.Split(out.String(), "\r") {
		if len(line) > 11 {
			t.Fatalf("%q would wrap", line)
		}
	}
}

func TestNoWaiterForPrograms(t *testing.T) {
	var out bytes.Buffer
	withEnv(t, map[string]string{})
	if w := startWaiting(&out, false, "x"); w != nil {
		t.Fatal("drew on something that isn't a terminal")
	}
	aTerminal(t)
	if w := startWaiting(&out, true, "x"); w != nil {
		t.Fatal("drew under --json")
	}
	for _, env := range []map[string]string{{"TERM": "dumb"}, {"CI": "true"}} {
		withEnv(t, env)
		if w := startWaiting(&out, false, "x"); w != nil {
			t.Fatalf("drew under %v", env)
		}
	}
	// A nil waiter is one that draws nothing: every method works.
	var none *waiter
	ran := false
	none.say("x")
	none.print(func() { ran = true })
	none.stop()
	if !ran || out.Len() != 0 {
		t.Fatalf("ran %v, wrote %q", ran, out.String())
	}
}

// On a terminal, `ask` keeps stdout exactly what it would be, and leaves
// stderr's line clean for what's said after it.
func TestAskOnATerminal(t *testing.T) {
	quick(t)
	aTerminal(t)
	s := newChatServer(t,
		step{state: "queued"},
		step{state: "working"},
		step{state: "idle", lines: []api.Line{{Seq: 3, From: "pony", Text: "It is 10."}}},
	)
	withEnv(t, with(aPonyTerminal, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL}))
	code, out, errOut := run("ask", "pepper", "what time is it?")
	if code != ExitOK || out != "It is 10.\n" {
		t.Fatalf("exit %d, stdout %q (stderr %q)", code, out, errOut)
	}
	if runtime.GOOS != "windows" && !strings.Contains(errOut, "\x1b[48;5;") {
		t.Fatalf("no pony on the terminal: %q", errOut)
	}
	if !strings.HasSuffix(errOut, "\x1b[?25h") && !strings.HasSuffix(errOut, "\r") {
		t.Fatalf("the drawing wasn't cleared at the end: %q", errOut[max(0, len(errOut)-60):])
	}
	noToken(t, out, errOut)
}

// A note after the wait starts on a clean line.
func TestWatchOnATerminalNoteAfterTheLine(t *testing.T) {
	quick(t)
	aTerminal(t)
	s := newChatServer(t, step{state: "working"}, step{state: "needs_you"})
	withEnv(t, with(aPonyTerminal, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL}))
	code, _, errOut := run("watch", "wch_aaaaaaaaaaaaaaaaaaaaaaaa")
	if code != ExitWaiting {
		t.Fatalf("exit %d: %q", code, errOut)
	}
	i := strings.LastIndex(errOut, "pony: waiting on you")
	if i < 1 || !(strings.HasSuffix(errOut[:i], "\x1b[?25h") || errOut[i-1] == '\r') {
		t.Fatalf("the note doesn't start on a clean line: %q", errOut[max(0, i-40):])
	}
}
