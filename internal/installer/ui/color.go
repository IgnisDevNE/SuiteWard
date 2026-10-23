package ui

import (
	"fmt"
	"strings"
)

// ColorLevel is how many colors the terminal takes.
type ColorLevel int

const (
	// ColorNone adds no color. A NO_COLOR environment runs the plain mode, so only tests draw the frame this way.
	ColorNone ColorLevel = iota
	// Color256 uses the nearest index of the 256-color palette.
	Color256
	// ColorTrue uses 24-bit color.
	ColorTrue
)

type rgb struct{ r, g, b int }

// The brand colors of assets/brand/README.md (Green), the muted blue-gray of secondary text, the warning amber and the red of a critical
// warning. The terminal's own background and foreground stay in place: Ink and Paper are never forced.
var (
	green = rgb{0x3D, 0xD6, 0xA0}
	muted = rgb{0x8F, 0xA3, 0xB5}
	amber = rgb{0xF2, 0xC4, 0x6D}
	red   = rgb{0xF2, 0x6B, 0x6B}
)

// tone is a color role of the screens.
type tone int

const (
	toneDefault tone = iota // the terminal's foreground
	toneGreen
	toneMuted
	toneAmber
	toneRed
)

func (t tone) rgb() rgb {
	switch t {
	case toneGreen:
		return green
	case toneMuted:
		return muted
	case toneAmber:
		return amber
	case toneRed:
		return red
	default:
		panic("ui: the default tone has no color of its own")
	}
}

// paint wraps text in the escape sequences of a tone (and bold); the default tone without bold stays as it is.
func (l ColorLevel) paint(t tone, bold bool, text string) string {
	if l == ColorNone || text == "" || (t == toneDefault && !bold) {
		return text
	}
	var codes []string
	if bold {
		codes = append(codes, "1")
	}
	if t != toneDefault {
		c := t.rgb()
		if l == ColorTrue {
			codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", c.r, c.g, c.b))
		} else {
			codes = append(codes, fmt.Sprintf("38;5;%d", nearest256(c)))
		}
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}

// cubeLevels are the intensities of one channel in the 6x6x6 color cube (indexes 16 to 231) of the 256-color palette.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// nearest256 returns the index of the 256-color palette closest to c: the nearest cube entry, or a gray of the ramp (232 to 255)
// when that is closer.
func nearest256(c rgb) int {
	ri, gi, bi := nearestLevel(c.r), nearestLevel(c.g), nearestLevel(c.b)
	best := 16 + 36*ri + 6*gi + bi
	bestDist := distance(c, rgb{cubeLevels[ri], cubeLevels[gi], cubeLevels[bi]})
	for k := range 24 {
		v := 8 + 10*k
		if d := distance(c, rgb{v, v, v}); d < bestDist {
			best, bestDist = 232+k, d
		}
	}
	return best
}

func nearestLevel(v int) int {
	best := 0
	for i, level := range cubeLevels {
		if abs(v-level) < abs(v-cubeLevels[best]) {
			best = i
		}
	}
	return best
}

func distance(a, b rgb) int {
	dr, dg, db := a.r-b.r, a.g-b.g, a.b-b.b
	return dr*dr + dg*dg + db*db
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
