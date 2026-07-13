package api

import (
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func gsWithStopTimeout(t int) *gameskill.Gameskill {
	gs := &gameskill.Gameskill{}
	gs.Startup.StopTimeout = t
	return gs
}

func TestStopTimeout(t *testing.T) {
	cases := []struct {
		set  int
		want int
	}{
		{0, defaultStopTimeout},  // unset → default
		{-5, defaultStopTimeout}, // invalid → default
		{90, 90},                 // rune value honoured (DayZ)
		{600, maxStopTimeout},    // above the cap → clamped
		{maxStopTimeout, maxStopTimeout},
	}
	for _, c := range cases {
		if got := stopTimeout(gsWithStopTimeout(c.set)); got != c.want {
			t.Errorf("stopTimeout(%d) = %d, want %d", c.set, got, c.want)
		}
	}
}
