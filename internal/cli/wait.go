package cli

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// What a command shows while it waits on a pony: the pony from the sign-in
// page ambling and grazing, as it does on the ranch, and what it's waiting
// for; a plain spinner on one line where a terminal can't show that. It goes
// to stderr and only to a terminal, so a pipe, a file or --json never sees it.
//
// The pony is coloured spaces and nothing else. Block characters would give
// it twice the detail, but terminals draw them unevenly: Apple's leaves gaps
// and seams round each one and shifts its colour, on any background. A
// cell's background has none of that.

const (
	nameCells = 16 // characters of a pony's name the line shows
	showAfter = 3 * time.Second
	waitRows  = ponyRows + 1 // the pony, and the words under it
	stage     = 72           // the most columns it walks across
	minCols   = ponyWidth + 8
	minRows   = waitRows + 3 // room left for what's above it
)

// waitEvery is how often the drawing is redrawn; tests shorten it. The pony
// walks a stride a frame, so this alone sets its pace.
var waitEvery = 160 * time.Millisecond

// How it passes the time, as on the ranch: a little way at a walk, then
// grazing a while. A stride is what its hooves cover in a frame of the walk.
const (
	stride               = 1.2    // columns
	strollMin, strollMax = 8, 24  // columns
	grazeMin, grazeMax   = 20, 45 // frames: 3 to 7 seconds
)

// isTerminal says whether w is a terminal; tests replace it.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// ponyInks are the pony's greys, as 256-colour numbers: every colour
// terminal has them, and they read on dark and light backgrounds alike.
var ponyInks = map[byte]int{'k': 16, 'c': 237, 'g': 244, 's': 249, 'm': 253, 'w': 231}

// ponyRow draws a row of the pony in coloured spaces.
func ponyRow(row string) string {
	var b strings.Builder
	set := "0" // every row starts as the last one ended
	for x := range len(row) {
		next := "0"
		if ink := ponyInks[row[x]]; ink != 0 {
			next = fmt.Sprintf("48;5;%d", ink)
		}
		if next != set {
			b.WriteString("\x1b[" + next + "m")
			set = next
		}
		b.WriteByte(' ')
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func mirrored(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// ponyDrawing is every frame drawn: facing right, then left.
func ponyDrawing() (d [2][ponyFrameCount][ponyRows]string) {
	for f, frame := range ponyFrames {
		for y, row := range frame {
			d[0][f][y] = ponyRow(row)
			d[1][f][y] = ponyRow(mirrored(row))
		}
	}
	return d
}

// amble is the pony passing the time: where it is on its stage, and what it's
// doing. next moves it on a frame.
type amble struct {
	between func(lo, hi int) int // a whole number from lo to hi
	x, to   float64              // where it is, and where it's walking to
	left    bool
	gait    int
	graze   int // frames of grazing left; 0 while it walks
	beat    int // frames it has spent grazing
}

// next is the coming frame on a stage of room columns: how far along the
// pony is, which way it faces, and which drawing to show.
func (a *amble) next(room int) (x int, left bool, frame int) {
	stage := float64(room)
	a.x, a.to = min(a.x, stage), min(a.to, stage) // the terminal may have narrowed
	if a.graze > 0 {
		a.graze--
		a.beat++
		if a.graze == 0 {
			a.to = a.spot(stage)
		}
		return int(a.x), a.left, grazeFrame(a.beat)
	}
	if a.to == a.x {
		a.graze = a.between(grazeMin, grazeMax)
		return int(a.x), a.left, grazing
	}
	a.left = a.to < a.x
	if a.left {
		a.x = max(a.x-stride, a.to)
	} else {
		a.x = min(a.x+stride, a.to)
	}
	a.gait++
	return int(a.x), a.left, a.gait % walkFrames
}

// spot is where to walk to next: a little way one side or the other, and the
// other if that's off the stage.
func (a *amble) spot(stage float64) float64 {
	d := float64(a.between(strollMin, strollMax))
	if a.between(0, 1) == 1 {
		d = -d
	}
	want := a.x + d
	if want < 0 || want > stage {
		want = a.x - d
	}
	return max(0, min(stage, want))
}

// grazeFrame is the drawing for a beat of grazing: head down, chewing every
// so often, and a swish of the tail now and then.
func grazeFrame(beat int) int {
	switch beat % 34 {
	case 16, 17:
		return tailOneWay
	case 18, 19:
		return tailOtherWay
	}
	if beat/3%3 == 0 {
		return chewing
	}
	return grazing
}

// ponyRoom is the columns the pony has to walk in, on a terminal this wide.
func ponyRoom(cols int) int {
	return min(cols, stage) - ponyWidth - 1
}

// spin is frame i of the plain spinner.
func spin(i int) string {
	return string("|/-\\"[i%4])
}

// canDrawPony says whether the terminal can show the pony: 256 colours and
// cursor movement, on a terminal big enough for it. Not on Windows, whose
// console needs asking first, nor the Linux text console, nor under NO_COLOR.
func canDrawPony(goos string, env func(string) string, cols, rows int) bool {
	term := env("TERM")
	if goos == "windows" || term == "linux" || env("NO_COLOR") != "" || cols < minCols || rows < minRows {
		return false
	}
	if env("COLORTERM") != "" {
		return true
	}
	for _, t := range []string{"256color", "kitty", "ghostty", "alacritty", "wezterm", "foot"} {
		if strings.Contains(term, t) {
			return true
		}
	}
	return false
}

// busyWords is what the line says about a state: "" for one that isn't
// worth saying.
func busyWords(pony, state string) string {
	if r := []rune(pony); len(r) > nameCells {
		pony = string(r[:nameCells-1]) + "…"
	}
	switch state {
	case "working":
		return pony + " is working"
	case "queued":
		return pony + " will get to this next"
	}
	return ""
}

// cut keeps the first n characters of s.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// elapsed is how long the wait has been, shortly.
func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// waiter draws until stop. A nil waiter draws nothing, which is what
// startWaiting gives anything but a terminal; its methods all work.
type waiter struct {
	out   io.Writer
	start time.Time
	quit  chan struct{}
	gone  chan struct{}
	once  sync.Once
	pony  *[2][ponyFrameCount][ponyRows]string // nil: the spinner
	amble amble

	mu    sync.Mutex
	text  string
	drawn int // the spinner's cells on the line; for the pony, whether it's up
}

// startWaiting starts drawing, saying text, unless the output is for a
// program: --json, or stderr isn't a terminal.
func startWaiting(stderr io.Writer, asJSON bool, text string) *waiter {
	if asJSON || !isTerminal(stderr) || getenv("TERM") == "dumb" || getenv("CI") != "" {
		return nil
	}
	w := &waiter{out: stderr, start: time.Now(), text: text,
		quit: make(chan struct{}), gone: make(chan struct{})}
	if cols, rows := termSize(stderr); canDrawPony(runtime.GOOS, getenv, cols, rows) {
		d := ponyDrawing()
		w.pony = &d
		w.amble.between = func(lo, hi int) int { return lo + rand.IntN(hi-lo+1) }
		w.amble.to = w.amble.spot(float64(ponyRoom(cols)))
		io.WriteString(stderr, "\x1b[?25l") // the cursor would flicker through the drawing
	}
	go w.run()
	return w
}

func (w *waiter) run() {
	defer close(w.gone)
	t := time.NewTicker(waitEvery)
	defer t.Stop()
	for i := 0; ; i++ {
		w.draw(i)
		select {
		case <-w.quit:
			return
		case <-t.C:
		}
	}
}

func (w *waiter) words() string {
	if d := time.Since(w.start); d >= showAfter {
		return w.text + "  " + elapsed(d)
	}
	return w.text
}

func (w *waiter) draw(i int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pony == nil {
		w.spinner(i)
		return
	}
	cols, rows := termSize(w.out)
	if cols < minCols || rows < minRows {
		// The terminal shrank under it.
		w.clear()
		io.WriteString(w.out, "\x1b[?25h")
		w.pony = nil
		w.spinner(i)
		return
	}
	x, left, frame := w.amble.next(ponyRoom(cols))
	face := 0
	if left {
		face = 1
	}
	pad := strings.Repeat(" ", x)
	var b strings.Builder
	for _, row := range w.pony[face][frame] {
		b.WriteString(pad + row + "\x1b[K\n")
	}
	// Back to the top of the drawing, where whatever comes next belongs.
	fmt.Fprintf(&b, "%s\x1b[K\n\x1b[%dA", cut(w.words(), cols-1), waitRows)
	io.WriteString(w.out, b.String())
	w.drawn = 1
}

// spinner overwrites the line. A carriage return and padding, not escape
// codes: they work on every console.
func (w *waiter) spinner(i int) {
	line := spin(i) + "  " + w.words()
	if cols, _ := termSize(w.out); cols > 1 {
		line = cut(line, cols-1) // a line that wraps can't be overwritten
	}
	n := utf8.RuneCountInString(line)
	fmt.Fprintf(w.out, "\r%s%s", line, strings.Repeat(" ", max(0, w.drawn-n)))
	w.drawn = n
}

func (w *waiter) clear() {
	if w.drawn == 0 {
		return
	}
	if w.pony != nil {
		io.WriteString(w.out, "\x1b[J")
	} else {
		fmt.Fprintf(w.out, "\r%s\r", strings.Repeat(" ", w.drawn))
	}
	w.drawn = 0
}

// say changes what the line says. A nil or blank text keeps it as it is.
func (w *waiter) say(text string) {
	if w == nil || text == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.text = text
}

// print runs fn with the drawing out of its way, for anything else that
// writes to the terminal. The next frame draws it again, under what fn wrote.
func (w *waiter) print(fn func()) {
	if w == nil {
		fn()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clear()
	fn()
}

// stop ends the animation, clears it and gives the cursor back. Anything
// written to the terminal after it starts on a clean line. Safe to call twice.
func (w *waiter) stop() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.quit)
		<-w.gone
		w.mu.Lock()
		defer w.mu.Unlock()
		w.clear()
		if w.pony != nil {
			io.WriteString(w.out, "\x1b[?25h")
		}
	})
}
