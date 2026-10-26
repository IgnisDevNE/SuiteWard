//go:build windows

package ui

import (
	"context"
	"os"
)

// inputRecord is the INPUT_RECORD of a key event: what the console queues for a key.
type inputRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	virtual   uint16
	scan      uint16
	char      uint16
	control   uint32
}

// inputReadiness is the wait for a key of the console behind f that Options.InputReady is, or nil where it cannot be done.
func inputReadiness(*os.File) func(context.Context) error { return nil }
