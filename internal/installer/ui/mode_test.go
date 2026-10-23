package ui

import "testing"

func TestEnvMode(t *testing.T) {
	good := Env{StdinTTY: true, StdoutTTY: true, Width: 80, Height: 24}
	with := func(mut func(*Env)) Env {
		e := good
		e.Vars = map[string]string{}
		mut(&e)
		return e
	}
	tests := []struct {
		name string
		env  Env
		want Mode
	}{
		{"a terminal on both ends", good, ModeFull},
		{"TERM set to something else", with(func(e *Env) { e.Vars["TERM"] = "xterm-256color" }), ModeFull},
		{"the smallest size that fits", with(func(e *Env) { e.Width, e.Height = 60, 20 }), ModeFull},
		{"stdin is not a terminal", with(func(e *Env) { e.StdinTTY = false }), ModePlain},
		{"stdout is not a terminal", with(func(e *Env) { e.StdoutTTY = false }), ModePlain},
		{"NO_COLOR with a value", with(func(e *Env) { e.Vars["NO_COLOR"] = "1" }), ModePlain},
		{"NO_COLOR set but empty counts as set", with(func(e *Env) { e.Vars["NO_COLOR"] = "" }), ModePlain},
		{"TERM=dumb", with(func(e *Env) { e.Vars["TERM"] = "dumb" }), ModePlain},
		{"the caller asked for plain", with(func(e *Env) { e.Plain = true }), ModePlain},
		{"one column too narrow", with(func(e *Env) { e.Width = 59 }), ModePlain},
		{"one row too short", with(func(e *Env) { e.Height = 19 }), ModePlain},
		{"size unknown", with(func(e *Env) { e.Width, e.Height = 0, 0 }), ModePlain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.Mode(); got != tt.want {
				t.Fatalf("Mode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvColor(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want ColorLevel
	}{
		{"COLORTERM=truecolor", map[string]string{"COLORTERM": "truecolor"}, ColorTrue},
		{"COLORTERM=24bit", map[string]string{"COLORTERM": "24bit"}, ColorTrue},
		{"Windows Terminal", map[string]string{"WT_SESSION": "0b2c-1"}, ColorTrue},
		{"COLORTERM with another value", map[string]string{"COLORTERM": "yes"}, Color256},
		{"nothing set", nil, Color256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Env{Vars: tt.vars}).Color(); got != tt.want {
				t.Fatalf("Color() = %v, want %v", got, tt.want)
			}
		})
	}
}
