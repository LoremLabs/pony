package api

import "strings"

// Inert makes text from elsewhere safe to show on a terminal: what a pony
// said, what a mail's subject was, what a web page put in either. A terminal
// acts on control characters, so left in they could move the cursor, rewrite
// what's on the screen, retitle the window or write to the clipboard. Newlines
// and tabs stay, since an answer has them. Out go the rest of C0, DEL, C1, and
// the marks that reorder text so it reads other than it is.
func Inert(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

func (p *Pony) inert() {
	p.Slug = Inert(p.Slug)
	p.Name = Inert(p.Name)
	p.Address = Inert(p.Address)
	p.Status = Inert(p.Status)
	p.Doing = Inert(p.Doing)
	p.Card = Inert(p.Card)
	p.Mood = Inert(p.Mood)
}

func (e *Event) inert() {
	e.Pony = Inert(e.Pony)
	e.Who = Inert(e.Who)
	e.Kind = Inert(e.Kind)
	e.Card = Inert(e.Card)
	e.Title = Inert(e.Title)
	e.Text = Inert(e.Text)
}

func (r *Card) inert() {
	r.Card.Pony = Inert(r.Card.Pony)
	if r.Hold != nil {
		r.Hold.ID = Inert(r.Hold.ID)
		r.Hold.Name = Inert(r.Hold.Name)
		r.Hold.Reason = Inert(r.Hold.Reason)
		r.Hold.Note = Inert(r.Hold.Note)
	}
	for i := range r.Lines {
		r.Lines[i].From = Inert(r.Lines[i].From)
		r.Lines[i].Text = Inert(r.Lines[i].Text)
		r.Lines[i].Via = Inert(r.Lines[i].Via)
	}
}
