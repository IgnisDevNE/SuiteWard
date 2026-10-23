package ui

import "testing"

// The brand colors are fixed by assets/brand/README.md; the 256-color indexes are the nearest cube or gray-ramp entries.
func TestNearest256(t *testing.T) {
	tests := []struct {
		name string
		c    rgb
		want int
	}{
		{"green #3DD6A0", green, 79},
		{"muted #8FA3B5", muted, 109},
		{"amber #F2C46D", amber, 221},
		{"black", rgb{0, 0, 0}, 16},
		{"white", rgb{255, 255, 255}, 231},
		{"a gray on the ramp", rgb{128, 128, 128}, 244},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nearest256(tt.c); got != tt.want {
				t.Fatalf("nearest256(%v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

func TestPaint(t *testing.T) {
	tests := []struct {
		name  string
		level ColorLevel
		tone  tone
		bold  bool
		want  string
	}{
		{"truecolor green", ColorTrue, toneGreen, false, "\x1b[38;2;61;214;160mx\x1b[0m"},
		{"256-color green", Color256, toneGreen, false, "\x1b[38;5;79mx\x1b[0m"},
		{"bold amber in truecolor", ColorTrue, toneAmber, true, "\x1b[1;38;2;242;196;109mx\x1b[0m"},
		{"bold with the default color", Color256, toneDefault, true, "\x1b[1mx\x1b[0m"},
		{"the default color is not painted", ColorTrue, toneDefault, false, "x"},
		{"no color adds nothing", ColorNone, toneGreen, true, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.level.paint(tt.tone, tt.bold, "x"); got != tt.want {
				t.Fatalf("paint = %q, want %q", got, tt.want)
			}
		})
	}
	if got := ColorTrue.paint(toneGreen, false, ""); got != "" {
		t.Fatalf("an empty text must stay empty, got %q", got)
	}
}
