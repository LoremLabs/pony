package look

// The pony from the sign-in page: its walk, then Grazing. Each letter is one
// terminal cell, a column of the web pony's pixels and two of its rows: the
// upper pixel if there is one, else the lower, and the eye always. PonyInks
// says what the letters are; "." is nothing there.
const (
	PonyWidth  = 28 // columns
	PonyRows   = 12
	WalkFrames = 8

	// The frames after the walk.
	Grazing        = WalkFrames
	Chewing        = WalkFrames + 1
	TailOneWay     = WalkFrames + 2
	TailOtherWay   = WalkFrames + 3
	PonyFrameCount = WalkFrames + 4
)

var PonyFrames = [PonyFrameCount][PonyRows]string{
	{
		".....................s.s....",
		"...................ggsssss..",
		".................gggssksswss",
		"...............gggsssssssssg",
		"....gssssssssgggssssssgsssg.",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		".ggggssssssssssssssssgg.....",
		".gg..sssgg......gg.ss.......",
		"..g...ggg.......gg.gg.......",
		".......gg......gg..gg.......",
		".......cc..........cc.......",
	},
	{
		"....................gssss...",
		"..................gggskswss.",
		"................gggsssssssws",
		"..............gggsssssssssgg",
		"...sgssssssssggssssssss.....",
		"..gsgssssgsggsssssssssss....",
		"..gsssssssssssgsgsssssss....",
		"..gg.ssggggggggggggssg......",
		"..gg.ssgg.......gggss.......",
		"...g..ggg........gggg.......",
		"......gg.........gggg.......",
		"......cc...........cc.......",
	},
	{
		"............................",
		"....................gssss...",
		"..................gggskswss.",
		"................gggsssssssws",
		"....gssssssssggggsssssssssgg",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		"..gggssssssssssssssssgg.....",
		"..gg.ssgg.......gggss.......",
		"...g.gggg........ggg........",
		".....gggg.........gg........",
		".....cc...........cc........",
	},
	{
		".....................s.s....",
		"...................ggsssss..",
		".................gggssksswss",
		"...............gggsssssssssg",
		"....gssssssssgggssssssgsssg.",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		"..gggssssssssssssssssgg.....",
		"..gg.ss.gg......ggsss.......",
		"...g.gg.gg.......ggg........",
		".....gg..gg......gg.........",
		".....cc..........cc.........",
	},
	{
		".....................s.s....",
		"...................ggsssss..",
		".................gggssksswss",
		"...............gggsssssssssg",
		"....gssssssssgggssssssgsssg.",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		".ggggssssssssssssssssgg.....",
		".gg..ss.ggg.....gg.ss.......",
		"..g.gg...gg.....gg.gg.......",
		"....gg....gg....gggg........",
		"....cc....cc....cc..........",
	},
	{
		"....................gssss...",
		"..................gggskswss.",
		"................gggsssssssws",
		"..............gggsssssssssgg",
		"...sgssssssssggssssssss.....",
		"..gsgssssgsggsssssssssss....",
		".ggsssssssssssgsgsssssss....",
		".gg..ssggggggggggggssg......",
		".gg.ss..ggg.....gg.ssg......",
		".g..gg...gg.....gg..gg......",
		"...gg....gg.....gg..gg......",
		"...cc....cc.....cc..........",
	},
	{
		"............................",
		"....................gssss...",
		"..................gggskswss.",
		"................gggsssssssws",
		"....gssssssssggggsssssssssgg",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		".gg.gssssssssssssssssgg.....",
		"ggg.ss..gg......gg.sss......",
		".g..gg..gg.....gg...gg......",
		"....gg..gg.....gg....gg.....",
		"........cc.....cc....cc.....",
	},
	{
		".....................s.s....",
		"...................ggsssss..",
		".................gggssksswss",
		"...............gggsssssssssg",
		"....gssssssssgggssssssgsssg.",
		"..gssssssssssssssgssssss....",
		"..gsssgsssssssssgsssssss....",
		".gg.gssssssssssssssssgg.....",
		".gg..ss.gg.....ggg.ss.......",
		".gg..gg.gg.....gg...gg......",
		"......gggg....gg....gg......",
		"........cc....cc....cc......",
	},
	// Grazing
	{
		"............................",
		"............................",
		"............................",
		"............................",
		"....gssssssssgggs...........",
		"..gssssssssssssssgggg.......",
		"..gsssgsssssssssgssssgggg...",
		".ggggssssssssssssssssggggggg",
		".gg..ss.gg......gg.ss.ssskss",
		"..g..gg.gg......gg.gg..ssssw",
		".....gg.gg......gg.gg..ssssw",
		".....cc.cc......cc.cc...ggg.",
	},
	// Chewing
	{
		"............................",
		"............................",
		"............................",
		"............................",
		"....gssssssssgggs...........",
		"..gssssssssssssssgggg.......",
		"..gsssgsssssssssgssssgggg...",
		".ggggssssssssssssssssggggggg",
		".gg..ss.gg......gg.ss.ssskss",
		"..g..gg.gg......gg.gg..ssssw",
		".....gg.gg......gg.gg..ssssw",
		".....cc.cc......cc.cc...gg.g",
	},
	// tail one way
	{
		"............................",
		"............................",
		"............................",
		"............................",
		"....gssssssssgggs...........",
		"..gssssssssssssssgggg.......",
		".ggsssgsssssssssgssssgggg...",
		".gg.gssssssssssssssssggggggg",
		"gg...ss.gg......gg.ss.ssskss",
		"gg...gg.gg......gg.gg..ssssw",
		".....gg.gg......gg.gg..ssssw",
		".....cc.cc......cc.cc...ggg.",
	},
	// tail the other
	{
		"............................",
		"............................",
		"............................",
		"............................",
		"....gssssssssgggs...........",
		"..gssssssssssssssgggg.......",
		"..gsssgsssssssssgssssgggg...",
		"..gggssssssssssssssssggggggg",
		"..gg.ss.gg......gg.ss.ssskss",
		"....ggg.gg......gg.gg..ssssw",
		".....gg.gg......gg.gg..ssssw",
		".....cc.cc......cc.cc...ggg.",
	},
}

// PonyInks are the pony's greys, as 256-colour numbers: every colour terminal
// has them, and they read on dark and light backgrounds alike.
var PonyInks = map[byte]int{'k': 16, 'c': 237, 'g': 244, 's': 249, 'm': 253, 'w': 231}
