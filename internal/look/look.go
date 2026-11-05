// Package look is what the command line's parts agree on about how things
// look: a pony's name is the same colour in the log and in a conversation.
package look

import (
	"fmt"
	"hash/crc32"
)

// Colour is one of the 256, plain or bold.
type Colour struct {
	Index int
	Bold  bool
}

// SGR is the colour as an escape code's parameters.
func (c Colour) SGR() string {
	if c.Bold {
		return fmt.Sprintf("1;38;5;%d", c.Index)
	}
	return fmt.Sprintf("38;5;%d", c.Index)
}

// Palette is what a pony's name can be: mid-tones of the 256 colours, to read
// on dark and light backgrounds alike, and none of the red and yellow that
// say a line needs a look. Each comes plain and bold, so fewer ponies share
// one. A name's colour goes by the length of this list, so changing it at all
// recolours every pony.
var Palette = []Colour{
	{Index: 33},  // azure
	{Index: 37},  // teal
	{Index: 61},  // slate
	{Index: 98},  // purple
	{Index: 134}, // orchid
	{Index: 139}, // mauve
	{Index: 168}, // rose
	{Index: 33, Bold: true},
	{Index: 37, Bold: true},
	{Index: 61, Bold: true},
	{Index: 98, Bold: true},
	{Index: 134, Bold: true},
	{Index: 139, Bold: true},
	{Index: 168, Bold: true},
}

// PonyColour is the same for a pony every time, on every machine.
func PonyColour(pony string) Colour {
	return Palette[crc32.ChecksumIEEE([]byte(pony))%uint32(len(Palette))]
}
