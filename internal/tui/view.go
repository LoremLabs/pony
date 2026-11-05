package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/look"
)

// sidePadding is the columns left clear either side of the conversation.
const sidePadding = 2

// composerMost is the tallest the composer grows, in rows.
const composerMost = 6

// prompt starts the line you type on.
const prompt = "> "

const (
	pageChat   = "chat"
	pagePicker = "picker"
	pageBoard  = "board"
)

// inTerminalColours has the screen use the terminal's own colours: tview's
// defaults paint it black, wrong on a light terminal.
func inTerminalColours() {
	themed.Do(func() {
		d := tcell.ColorDefault
		s := &tview.Styles
		s.PrimitiveBackgroundColor, s.ContrastBackgroundColor, s.MoreContrastBackgroundColor = d, d, d
		s.BorderColor, s.TitleColor, s.GraphicsColor = d, d, d
		s.PrimaryTextColor, s.SecondaryTextColor, s.TertiaryTextColor = d, d, d
		s.InverseTextColor, s.ContrastSecondaryTextColor = d, d
	})
}

var themed sync.Once

// safe is the one way remote words reach the screen. Inert takes out what a
// terminal would act on; Escape what the conversation view would read as its
// own markup, so "[red]" in a reply is just those letters.
func safe(s string) string {
	return tview.Escape(api.Inert(s))
}

// view is everything on the screen. Its widgets belong to tview's goroutine:
// the session reaches them through do.
type view struct {
	app      *tview.Application
	pages    *tview.Pages
	chat     *tview.Flex
	header   *tview.TextView
	place    *tview.TextView
	conv     *tview.TextView
	status   *tview.TextView
	composer *tview.TextArea
	menu     *tview.TextView
	pick     *tview.Flex
	drawing  *tview.Box
	picker   *tview.List
	welcome  *tview.TextView
	pickHint *tview.TextView
	board    *tview.TextView
	boardNow *tview.TextView
	plain    bool // NO_COLOR

	entries []entry // what's in the conversation, to wrap again on a resize
	width   int     // the columns they're wrapped to
	lanes   []api.Lane
	height  int

	stopped chan struct{}
	once    sync.Once

	// On tview's goroutine only.
	onSubmit func(text string)
	onOpen   func()
	onBack   func()    // leaving the board
	warned   bool      // a draft has been warned about
	matches  []Command // what the / menu offers now
	menuAt   int       // which of them is picked
	menuFor  string    // the text it was made for
	menuShut bool      // Esc put it away, for this text
	history  []string  // what's been said here, for Up
	recall   int       // how far back Up has gone
}

func newView(plain bool) *view {
	inTerminalColours()
	v := &view{app: tview.NewApplication(), pages: tview.NewPages(), plain: plain, stopped: make(chan struct{})}

	v.header = tview.NewTextView().SetDynamicColors(true).SetText("OnePony")
	v.place = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	top := tview.NewFlex().AddItem(v.header, 0, 1, false).AddItem(v.place, 0, 3, false)

	v.conv = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true).SetScrollable(true)
	v.status = tview.NewTextView().SetDynamicColors(true)

	v.composer = tview.NewTextArea().
		SetLabel(prompt).
		SetLabelStyle(tcell.StyleDefault.Bold(true)).
		SetTextStyle(tcell.StyleDefault).
		SetPlaceholderStyle(tcell.StyleDefault.Dim(true))

	rule := func() *tview.Box { return tview.NewBox().SetDrawFunc(drawRule) }

	v.menu = tview.NewTextView().SetDynamicColors(true)

	v.chat = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(top, 1, 0, false).
		AddItem(rule(), 1, 0, false).
		AddItem(v.conv, 0, 1, false).
		AddItem(v.status, 1, 0, false).
		AddItem(rule(), 1, 0, false).
		AddItem(v.composer, 1, 0, true).
		AddItem(v.menu, 0, 0, false)
	v.chat.SetBorderPadding(0, 0, sidePadding, sidePadding)

	v.composer.SetChangedFunc(v.fitComposer)
	v.composer.SetInputCapture(v.composerKey)

	v.welcome = tview.NewTextView().SetDynamicColors(true)
	v.picker = tview.NewList().ShowSecondaryText(false).SetWrapAround(true).
		SetHighlightFullLine(false).SetSelectedFocusOnly(false).
		SetMainTextStyle(tcell.StyleDefault).
		SetSelectedStyle(tcell.StyleDefault)
	v.pickHint = tview.NewTextView().SetDynamicColors(true)
	v.drawing = tview.NewBox()
	v.drawing.SetDrawFunc(v.drawPony)
	v.pick = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.drawing, 0, 0, false).
		AddItem(v.welcome, 4, 0, false).
		AddItem(v.picker, 0, 1, true).
		AddItem(v.pickHint, 1, 0, false)
	v.pick.SetBorderPadding(1, 0, sidePadding, sidePadding)

	v.board = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(false)
	v.boardNow = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	boardTop := tview.NewFlex().
		AddItem(tview.NewTextView().SetText("OnePony"), 0, 1, false).
		AddItem(v.boardNow, 0, 3, false)
	boardPage := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(boardTop, 1, 0, false).
		AddItem(rule(), 1, 0, false).
		AddItem(v.board, 0, 1, true)
	boardPage.SetBorderPadding(0, 0, sidePadding, sidePadding)

	v.pages.AddPage(pagePicker, v.pick, true, false)
	v.pages.AddPage(pageBoard, boardPage, true, false)
	v.pages.AddPage(pageChat, v.chat, true, true)

	v.app.EnablePaste(true)
	v.app.SetInputCapture(v.appKey)
	v.app.SetBeforeDrawFunc(v.fit)
	v.app.SetRoot(v.pages, true).SetFocus(v.composer)
	return v
}

// do runs f on tview's goroutine and waits, or gives up once the screen has
// gone. QueueUpdateDraw alone would wait for ever after Stop.
func (v *view) do(f func()) {
	done := make(chan struct{})
	go func() {
		v.app.QueueUpdateDraw(f)
		close(done)
	}()
	select {
	case <-done:
	case <-v.stopped:
	}
}

func (v *view) stop() {
	v.once.Do(func() { close(v.stopped) })
}

// inColour is words in a pony's own colour.
func (v *view) inColour(pony, words string) string {
	c := look.PonyColour(pony)
	attr := ""
	if c.Bold {
		attr = "b"
	}
	if v.plain {
		return fmt.Sprintf("[::b]%s[-::-]", words)
	}
	return fmt.Sprintf("[#%06x::%s]%s[-::-]", tcell.PaletteColor(c.Index).Hex(), attr, words)
}

// nameTag is a pony's name in its own colour.
func (v *view) nameTag(pony, name string) string {
	return v.inColour(pony, safe(name))
}

// addressTag is a pony's address, dim: it's who, not what was said.
func addressTag(address string) string {
	if address == "" {
		return ""
	}
	return "[::d]" + safe("<"+address+">") + "[-::-]"
}

// pony is who the conversation is with.
type pony struct {
	Slug, Name string
}

// Who's speaking decides how a turn looks. The pony you're talking to needs no
// name: a bar in its colour runs down its answer. Your own lines start with a
// coloured dash and are grey. Anyone else is named, with their words set in
// under it.
const (
	fromPony = iota
	fromYou
	fromOther
	note
	activity
	trouble
	request
)

// otherIndent is how far someone else's words sit in from their name.
const otherIndent = "   "

// yourColour is the dash that starts your lines: a green, which no pony's name
// takes and nothing else on the screen means anything by.
const yourColour = "#5faf5f"

// entry is one turn in the conversation.
type entry struct {
	kind int
	slug string // the pony, for its colour
	name string // someone else's
	text string
	note string // a request: what went wrong the last time
	link string // a request: where to decide it
}

// wrapLine breaks a line at spaces to fit width, or within a word that's wider
// than the whole of it.
func wrapLine(line string, width int) []string {
	if width < 1 || runewidth.StringWidth(line) <= width {
		return []string{line}
	}
	var rows []string
	row, used := "", 0
	flush := func() {
		rows = append(rows, strings.TrimRight(row, " "))
		row, used = "", 0
	}
	for _, word := range strings.SplitAfter(line, " ") {
		w := runewidth.StringWidth(strings.TrimRight(word, " "))
		if used > 0 && used+w > width {
			flush()
		}
		for w > width {
			cut := runewidth.Truncate(word, width-used, "")
			if cut == "" {
				cut = string([]rune(word)[:1]) // wider than a row: it wraps anyway
			}
			row += cut
			word = strings.TrimPrefix(word, cut)
			w = runewidth.StringWidth(strings.TrimRight(word, " "))
			flush()
		}
		row += word
		used += runewidth.StringWidth(word)
	}
	if strings.TrimSpace(row) != "" {
		flush()
	}
	return rows
}

// wrapText is what was said as rows of width, ready for the screen: inert, and
// with nothing in it the conversation view would take for its own markup.
func wrapText(text string, width int) []string {
	text = strings.ReplaceAll(api.Inert(text), "\t", "    ")
	var rows []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		for _, row := range wrapLine(line, width) {
			rows = append(rows, tview.Escape(row))
		}
	}
	return rows
}

// format is an entry as it goes on the screen. The rows are wrapped here, not
// by the view, so what starts a line starts every line.
func (v *view) format(e entry) string {
	width := max(v.width, 20)
	var b strings.Builder
	switch e.kind {
	case fromPony:
		bar := v.inColour(e.slug, string(tview.BoxDrawingsHeavyVertical))
		for _, row := range wrapText(e.text, width-2) {
			b.WriteString(strings.TrimRight(bar+" "+row, " ") + "\n")
		}
	case fromYou:
		dash := "[" + yourColour + "]-[-]"
		if v.plain {
			dash = "-"
		}
		for _, row := range wrapText(e.text, width-2) {
			if row == "" {
				b.WriteString(dash + "\n")
				continue
			}
			b.WriteString(dash + " [::d]" + row + "[-::-]\n")
		}
	case fromOther:
		b.WriteString("[::b]" + safe(e.name) + "[-::-]\n")
		for _, row := range wrapText(e.text, width-len(otherIndent)) {
			b.WriteString(otherIndent + row + "\n")
		}
	case trouble:
		if v.plain {
			b.WriteString("[::b]✗ " + safe(e.text) + "[-::-]\n")
		} else {
			b.WriteString("[red]✗ " + safe(e.text) + "[-]\n")
		}
	case request:
		if v.plain {
			b.WriteString("[::b]… Waiting for your go-ahead[-::-]\n")
		} else {
			b.WriteString("[yellow::b]… Waiting for your go-ahead[-::-]\n")
		}
		for _, row := range wrapText(e.text, width-len(otherIndent)) {
			b.WriteString(otherIndent + row + "\n")
		}
		if e.note != "" {
			lead := "[red]"
			if v.plain {
				lead = "[::b]"
			}
			for _, row := range wrapText("It didn't go last time: "+e.note, width-len(otherIndent)) {
				b.WriteString(otherIndent + lead + row + "[-::-]\n")
			}
		}
		b.WriteString(otherIndent + "[::d]Ctrl+O opens it in your browser[-::-]\n")
		for _, row := range wrapText(e.link, width-len(otherIndent)) {
			b.WriteString(otherIndent + "[::d]" + row + "[-::-]\n")
		}
	default: // a note, or something a pony did: dim, and never the conversation's own words
		b.WriteString("[::d]" + safe(e.text) + "[-::-]\n")
	}
	return b.String() + "\n"
}

func (v *view) add(e entry) {
	v.entries = append(v.entries, e)
	fmt.Fprint(v.conv, v.format(e))
}

// clear empties the conversation on this screen, and nothing anywhere else.
func (v *view) clear() {
	v.entries = nil
	v.conv.Clear()
	v.repaint()
}

// repaint has the terminal draw every cell again. Replacing a cell isn't always
// enough: box characters reach a little past their cell, so stacked bars join,
// and on Apple's Terminal what's past it stayed behind as a dot when the cell
// was blanked. Not for every keystroke: when the conversation changes. In the
// background, because the screen's own goroutine can't wait on its own queue.
func (v *view) repaint() {
	go v.app.Sync()
}

// render writes it all again, for a new width.
func (v *view) render() {
	v.conv.Clear()
	for _, e := range v.entries {
		fmt.Fprint(v.conv, v.format(e))
	}
	v.conv.ScrollToEnd()
	v.repaint()
}

// line is someone's turn in the conversation.
func (v *view) line(p pony, l api.Line) {
	switch l.From {
	case "pony":
		v.add(entry{kind: fromPony, slug: p.Slug, text: l.Text})
	case "you":
		v.add(entry{kind: fromYou, text: l.Text})
	default:
		v.add(entry{kind: fromOther, name: l.From, text: l.Text})
	}
}

// note is something for you rather than from anyone.
func (v *view) note(text string) {
	v.add(entry{kind: note, text: text})
}

// event is something a pony did, or something that went wrong: trouble is red,
// as it is in the log. The chat's own title would only repeat itself here.
func (v *view) event(e api.Event) {
	text := strings.TrimSuffix(e.Text, " · "+e.Title)
	switch e.Kind {
	case "abandoned", "unsent":
		v.add(entry{kind: trouble, text: text})
	case "resumed", "declined":
		v.add(entry{kind: activity, text: e.Who + " " + text})
	default:
		v.add(entry{kind: activity, text: text})
	}
}

// hold is what a pony is waiting on you for, in the words it was held with.
func (v *view) hold(h *api.Hold, link string) {
	why := h.Reason
	if why == "" {
		why = h.Name
	}
	v.add(entry{kind: request, text: strings.ReplaceAll(why, "in their name", "in your name"), note: h.Note, link: link})
}

// setPlace is the top right: who, and how it's doing.
func (v *view) setPlace(p *api.Pony, word string) {
	text := v.nameTag(p.Slug, p.Slug)
	if tag := addressTag(p.Address); tag != "" {
		text += " " + tag
	}
	v.place.SetText(text + "  [::d]" + safe(word) + "[-::-]")
}

// statusWord is a pony's state in a word, for a pony with no mood yet.
func statusWord(p *api.Pony) string {
	if !p.Ready {
		return "getting ready"
	}
	switch p.Status {
	case "needs_you":
		return "needs you"
	}
	return p.Status
}

func (v *view) setStatus(text string) {
	if text == "" {
		v.status.SetText("")
		return
	}
	v.status.SetText("[::d]" + safe(text) + "[-::-]")
}

// showBoard puts the board on the screen, and drawBoard fills it in. Each read
// replaces it whole, keeping your place if you've scrolled.
func (v *view) showBoard() {
	v.pages.SwitchToPage(pageBoard)
	v.app.SetFocus(v.board)
	v.repaint()
}

// setBoard is what a read found, and word how the reads are going.
func (v *view) setBoard(lanes []api.Lane, word string) {
	if lanes != nil {
		v.lanes = lanes
		v.drawBoard()
	}
	v.boardNow.SetText("Board  [::d]" + word + "   Esc back[-::-]")
}

func (v *view) drawBoard() {
	row, _ := v.board.GetScrollOffset()
	// Two rows for the top of the page.
	v.board.SetText(renderBoard(v.lanes, v.width, v.height-2, v.plain))
	v.board.ScrollTo(row, 0)
}

func (v *view) showChat() {
	v.pages.SwitchToPage(pageChat)
	v.app.SetFocus(v.composer)
	v.repaint()
}

// showPicker offers the ponies, with the given one highlighted.
func (v *view) showPicker(ponies []api.Pony, first bool, current string, choose func(slug string), cancel func()) {
	v.pickHint.SetText("[::d]↑↓ select   Enter open   Esc quit[-::-]")
	if current != "" && !first {
		v.pickHint.SetText("[::d]↑↓ select   Enter open   Esc back[-::-]")
	}
	if first {
		v.welcome.SetText("[::b]Welcome to OnePony.[-::-]\n\nWhich pony would you like to talk to?")
	} else {
		v.welcome.SetText("[::b]Which pony would you like to talk to?[-::-]")
	}
	v.picker.SetChangedFunc(nil)
	v.picker.Clear()
	names, words := 0, 0
	for i := range ponies {
		names = max(names, len(ponies[i].Slug))
		words = max(words, len(ponyWord(&ponies[i])))
	}
	// A row's own colour, with the arrow on the one you're on.
	row := func(i int, on bool) string {
		p := &ponies[i]
		mark := "  "
		if on {
			mark = v.inColour(p.Slug, "❯") + " "
		}
		text := mark + v.nameTag(p.Slug, p.Slug) + strings.Repeat(" ", names-len(p.Slug)+2) +
			"[::d]" + safe(ponyWord(p)) + "[-::-]" + strings.Repeat(" ", words-len(ponyWord(p))+2)
		return text + addressTag(p.Address)
	}
	at := 0
	for i := range ponies {
		if ponies[i].Slug == current {
			at = i
		}
	}
	for i := range ponies {
		v.picker.AddItem(row(i, i == at), "", 0, nil)
	}
	v.picker.SetCurrentItem(at)
	was := at
	v.picker.SetChangedFunc(func(i int, _, _ string, _ rune) {
		v.picker.SetItemText(was, row(was, false), "")
		v.picker.SetItemText(i, row(i, true), "")
		was = i
	})
	v.picker.SetSelectedFunc(func(i int, _, _ string, _ rune) { choose(ponies[i].Slug) })
	v.picker.SetDoneFunc(cancel)
	v.pages.SwitchToPage(pagePicker)
	v.app.SetFocus(v.picker)
	v.repaint()
}

// ponyWord is how a pony is doing, in a word: what it's at if it's at
// something, else its mood.
func ponyWord(p *api.Pony) string {
	if p.Status == "working" || p.Status == "needs_you" || p.Mood == "" {
		return statusWord(p)
	}
	return p.Mood
}

// placeWord is the word at the top right. What this chat is doing is known at
// once, from the card; otherwise it's the pony's own, as last read.
func placeWord(p *api.Pony, state string) string {
	switch state {
	case "working":
		return "working"
	case "queued":
		return "next up"
	case "needs_you":
		return "needs you"
	}
	return ponyWord(p)
}

// drawRule is the line between the conversation and what you're typing.
func drawRule(screen tcell.Screen, x, y, width, _ int) (int, int, int, int) {
	style := tcell.StyleDefault.Dim(true)
	for i := range width {
		screen.SetContent(x+i, y, tview.BoxDrawingsLightHorizontal, nil, style)
	}
	return x, y, width, 0
}

// drawPony is the pony from the waiting line, in coloured spaces: no glyphs,
// so it's as clean here as there.
func (v *view) drawPony(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
	for r, row := range look.PonyFrames[0] {
		if r >= height {
			break
		}
		for c := 0; c < len(row) && c < width; c++ {
			if ink := look.PonyInks[row[c]]; ink != 0 {
				screen.SetContent(x+c, y+r, ' ', nil, tcell.StyleDefault.Background(tcell.PaletteColor(ink)))
			}
		}
	}
	return x, y, width, height
}

// fit runs before every draw. The conversation wraps to the screen's width,
// and the pony gets its room when there's room for it and the list too, since
// the list is what that page is for. No colour, no pony.
func (v *view) fit(screen tcell.Screen) bool {
	width, height := screen.Size()
	if w := width - 2*sidePadding; w != v.width {
		v.width = w
		v.render()
		v.drawBoard()
	}
	if height != v.height {
		v.height = height
		v.drawBoard()
	}
	rows := 0
	if !v.plain && height >= look.PonyRows+v.picker.GetItemCount()+8 {
		rows = look.PonyRows + 1
	}
	v.pick.ResizeItem(v.drawing, rows, 0)
	return false
}

// updateMenu is the / menu following what's typed.
func (v *view) updateMenu() {
	text := v.composer.GetText()
	if text != v.menuFor {
		v.menuFor, v.menuAt, v.menuShut = text, 0, false
	}
	v.matches = nil
	if !v.menuShut {
		v.matches = matchCommands(text)
	}
	v.drawMenu()
}

func (v *view) drawMenu() {
	var rows []string
	for i, c := range v.matches {
		mark := "  "
		name := "/" + c.Name
		if i == v.menuAt {
			mark, name = "❯ ", "[::b]"+name+"[-::-]"
		}
		pad := strings.Repeat(" ", 8-len(c.Name))
		rows = append(rows, mark+name+pad+"[::d]"+c.About+"[-::-]")
	}
	v.menu.SetText(strings.Join(rows, "\n"))
	v.chat.ResizeItem(v.menu, len(rows), 0)
}

// menuKey is the / menu's turn at a key: true if it took it.
func (v *view) menuKey(ev *tcell.EventKey) bool {
	n := len(v.matches)
	if n == 0 {
		return false
	}
	switch ev.Key() {
	case tcell.KeyUp:
		v.menuAt = (v.menuAt + n - 1) % n
		v.drawMenu()
	case tcell.KeyDown:
		v.menuAt = (v.menuAt + 1) % n
		v.drawMenu()
	case tcell.KeyTab:
		v.composer.SetText("/"+v.matches[v.menuAt].Name, true)
	case tcell.KeyEnter:
		if ev.Modifiers() != tcell.ModNone {
			return false
		}
		if v.onSubmit != nil {
			v.onSubmit("/" + v.matches[v.menuAt].Name)
		}
	case tcell.KeyEscape:
		v.menuShut = true
		v.updateMenu()
	default:
		return false
	}
	return true
}

// fitComposer grows it with what's in it, rows as they'll wrap.
func (v *view) fitComposer() {
	v.warned = false
	v.updateMenu()
	_, _, width, _ := v.composer.GetInnerRect()
	width -= len(prompt)
	rows := 0
	for _, l := range strings.Split(v.composer.GetText(), "\n") {
		n := 1
		if width > 0 {
			n += (tview.TaggedStringWidth(tview.Escape(l)) - 1) / width
		}
		rows += max(n, 1)
	}
	height := min(max(rows, 1), composerMost)
	v.chat.ResizeItem(v.composer, height, 0)
	// It scrolled for the height it had: a paste lands in one row, so only the
	// last line would show in the room it now has. Set that right once this
	// edit is done, since tview settles the scroll at the very end of it.
	if rows > 1 {
		go v.do(func() {
			if row, _ := v.composer.GetOffset(); row > rows-height {
				v.composer.SetOffset(rows-height, 0)
			}
		})
	}
}

// scroll hands a paging key to the conversation.
func (v *view) scroll(ev *tcell.EventKey) {
	v.conv.InputHandler()(ev, func(tview.Primitive) {})
}

func (v *view) composerKey(ev *tcell.EventKey) *tcell.EventKey {
	if v.menuKey(ev) {
		return nil
	}
	switch ev.Key() {
	case tcell.KeyEnter:
		// Enter with a modifier, for a terminal that says so. Many send the
		// same as Enter, and that's a line sent.
		if ev.Modifiers() != tcell.ModNone {
			return tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
		}
		text := v.composer.GetText()
		if v.onSubmit != nil && strings.TrimSpace(text) != "" {
			v.recall = 0
			v.onSubmit(text)
		}
		return nil
	case tcell.KeyPgUp, tcell.KeyPgDn:
		v.scroll(ev)
		return nil
	case tcell.KeyUp:
		// Only from an empty composer, or one showing what Up already brought
		// back: with words in it, Up moves the cursor.
		if v.composer.GetTextLength() == 0 || v.recall > 0 {
			if v.recall < len(v.history) {
				v.recall++
				v.composer.SetText(v.history[len(v.history)-v.recall], true)
			}
			return nil
		}
	case tcell.KeyDown:
		if v.recall > 0 {
			v.recall--
			if v.recall == 0 {
				v.composer.SetText("", false)
			} else {
				v.composer.SetText(v.history[len(v.history)-v.recall], true)
			}
			return nil
		}
	}
	return ev
}

// appKey comes before anything else. Esc, Ctrl+C and Ctrl+D all leave, and
// ask first if that would lose what you were writing. On the picker, Esc is
// the list's: back to the conversation if there is one.
func (v *view) appKey(ev *tcell.EventKey) *tcell.EventKey {
	page, _ := v.pages.GetFrontPage()
	switch ev.Key() {
	case tcell.KeyCtrlO:
		if page != pagePicker && v.onOpen != nil {
			v.onOpen()
		}
		return nil
	case tcell.KeyEscape:
		if page == pageBoard {
			v.leaveBoard()
			return nil
		}
		if page == pagePicker || len(v.matches) > 0 {
			return ev
		}
	case tcell.KeyCtrlC, tcell.KeyCtrlD:
	default:
		return ev
	}
	if strings.TrimSpace(v.composer.GetText()) != "" && !v.warned {
		v.warned = true
		v.setStatus("You have something unsent. Press that again to leave without it.")
		return nil
	}
	v.app.Stop()
	return nil
}

func (v *view) leaveBoard() {
	if v.onBack != nil {
		v.onBack()
	}
	v.showChat()
}

// clock is how the session tells the time; tests don't replace it, but one
// name keeps every use of it together.
var clock = time.Now
