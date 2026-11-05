package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"github.com/loremlabs/pony/internal/api"
	"github.com/loremlabs/pony/internal/look"
)

const (
	boardGap       = 2  // columns between lanes
	boardNarrowest = 14 // a lane narrower than this reads badly: stack them
	cardRows       = 3  // a card's title, its pony, and a space
)

// A card's badge, as the board on the web words it. The first two wait on you.
var badgeWords = map[string]string{
	"needs_you": "needs you",
	"asked":     "asked you",
	"waiting":   "waiting",
	"stopped":   "stopped",
}

// cell is one row of one lane: what shows, kept apart from its colours so a
// lane can be padded by what shows.
type cell struct {
	text   string
	markup string
}

func styled(open, text string) cell {
	if text == "" {
		return cell{}
	}
	if open == "" {
		return cell{text, tview.Escape(text)}
	}
	return cell{text, open + tview.Escape(text) + "[-::-]"}
}

// ponyTag is a pony's own colour.
func ponyTag(pony string, plain bool) string {
	if plain {
		return ""
	}
	c := look.PonyColour(pony)
	attr := ""
	if c.Bold {
		attr = "b"
	}
	return fmt.Sprintf("[#%06x::%s]", tcell.PaletteColor(c.Index).Hex(), attr)
}

// heading is a lane's name and how many it holds.
func heading(l api.Lane) cell {
	return styled("[::b]", fmt.Sprintf("%s %d", l.Name, l.Count))
}

// more says how much of a lane isn't shown.
func more(n int) cell {
	return styled("[::d]", fmt.Sprintf("+ %d more", n))
}

// byLine is a card's second row: whose it is, and why it's where it is.
func byLine(c api.BoardCard, width int, plain bool) cell {
	word := badgeWords[c.Badge]
	tag := "[::d]"
	if c.Badge == "needs_you" || c.Badge == "asked" {
		tag = "[::b]"
	}
	if word == "" {
		return styled(ponyTag(c.Pony, plain), runewidth.Truncate(c.Pony, width, "…"))
	}
	if c.Pony == "" {
		return styled(tag, word)
	}
	pony := styled(ponyTag(c.Pony, plain), runewidth.Truncate(c.Pony, max(width-len(word)-2, 1), "…"))
	return cell{pony.text + "  " + word, pony.markup + "  " + styled(tag, word).markup}
}

// cardCells is a card in a lane this wide.
func cardCells(c api.BoardCard, width int, plain bool) []cell {
	title := runewidth.Truncate(strings.Join(strings.Fields(c.Title), " "), width, "…")
	return []cell{styled("", title), byLine(c, width, plain), {}}
}

// renderColumns is the board side by side, each lane cut to the rows there are,
// with how many it left out.
func renderColumns(lanes []api.Lane, width, height int, plain bool) string {
	n := len(lanes)
	laneWidth := (width - boardGap*(n-1)) / n
	room := max((height-2)/cardRows, 1)
	columns := make([][]cell, n)
	tallest := 0
	for i, l := range lanes {
		columns[i] = []cell{heading(l), {}}
		show := l.Cards
		cut := len(l.Cards) > room || l.Count > len(l.Cards) && len(l.Cards) >= room
		if cut {
			show = l.Cards[:max(room-1, 0)]
		}
		for _, c := range show {
			columns[i] = append(columns[i], cardCells(c, laneWidth, plain)...)
		}
		if left := l.Count - len(show); left > 0 {
			columns[i] = append(columns[i], more(left))
		}
		tallest = max(tallest, len(columns[i]))
	}
	var b strings.Builder
	for row := range tallest {
		var line strings.Builder
		for i, column := range columns {
			var c cell
			if row < len(column) {
				c = column[row]
			}
			line.WriteString(c.markup)
			if i < n-1 {
				line.WriteString(strings.Repeat(" ", laneWidth-runewidth.StringWidth(c.text)+boardGap))
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
	return b.String()
}

// renderStacked is the board one lane under the next, for a screen too narrow
// for them to stand side by side. The view scrolls, so nothing's left out but
// what the server didn't send.
func renderStacked(lanes []api.Lane, width int, plain bool) string {
	var b strings.Builder
	for _, l := range lanes {
		b.WriteString(heading(l).markup + "\n")
		for _, c := range l.Cards {
			rows := cardCells(c, width, plain)
			b.WriteString(rows[0].markup + "\n")
			if rows[1].text != "" {
				b.WriteString(rows[1].markup + "\n")
			}
		}
		if left := l.Count - len(l.Cards); left > 0 {
			b.WriteString(more(left).markup + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderBoard is the board for a screen this size: every lane side by side if
// they fit, then the lanes with something in them, then one under the next.
func renderBoard(lanes []api.Lane, width, height int, plain bool) string {
	if fits(lanes, width) {
		return renderColumns(lanes, width, height, plain)
	}
	var some []api.Lane
	for _, l := range lanes {
		if l.Count > 0 {
			some = append(some, l)
		}
	}
	if len(some) == 0 {
		some = lanes
	}
	if fits(some, width) {
		return renderColumns(some, width, height, plain)
	}
	return renderStacked(some, width, plain)
}

func fits(lanes []api.Lane, width int) bool {
	n := len(lanes)
	return n > 0 && (width-boardGap*(n-1))/n >= boardNarrowest
}
