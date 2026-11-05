package look

import (
	"hash/crc32"
	"testing"
)

func TestPonyColour(t *testing.T) {
	// crc32(name) % colours, so a pony's colour is the same on every machine.
	// A change to the list recolours every pony: these say so.
	for _, pony := range []string{"pepper", "juniper"} {
		if got, want := PonyColour(pony), Palette[crc32.ChecksumIEEE([]byte(pony))%14]; got != want {
			t.Errorf("%s: got %v, want %v", pony, got, want)
		}
	}
	seen := map[Colour]bool{}
	for _, p := range []string{"pepper", "juniper", "moss", "fern", "sophie", "emmy", "barley", "clover", "rye"} {
		seen[PonyColour(p)] = true
	}
	if len(seen) < 4 {
		t.Fatalf("too few colours for nine ponies: %v", seen)
	}
	half := len(Palette) / 2
	for i, c := range Palette[:half] {
		if c.Bold || Palette[half+i] != (Colour{Index: c.Index, Bold: true}) {
			t.Fatalf("%v has no bold twin", c)
		}
	}
}

func TestSGR(t *testing.T) {
	if got := (Colour{Index: 33}).SGR(); got != "38;5;33" {
		t.Fatal(got)
	}
	if got := (Colour{Index: 33, Bold: true}).SGR(); got != "1;38;5;33" {
		t.Fatal(got)
	}
}
