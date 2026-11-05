package cli

import "github.com/loremlabs/pony/internal/look"

// The pony's drawings live in look, shared with the conversation.
const (
	ponyWidth      = look.PonyWidth
	ponyRows       = look.PonyRows
	walkFrames     = look.WalkFrames
	grazing        = look.Grazing
	chewing        = look.Chewing
	tailOneWay     = look.TailOneWay
	tailOtherWay   = look.TailOtherWay
	ponyFrameCount = look.PonyFrameCount
)

var (
	ponyFrames = look.PonyFrames
	ponyInks   = look.PonyInks
)
