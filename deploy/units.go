// Package deploy embeds the unit files the installer renders: the quadlet units of the isolated tier's stack, and the systemd user unit of its
// executor.
package deploy

import "embed"

// Quadlet holds the unit files of deploy/quadlet under the prefix "quadlet/".
//
//go:embed quadlet/*
var Quadlet embed.FS

// Systemd holds the plain systemd units of deploy/systemd under the prefix "systemd/". They are not quadlets: nothing of the stack's directory,
// its dry run or its adoption reads them.
//
//go:embed systemd/*
var Systemd embed.FS
