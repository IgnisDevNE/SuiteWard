// Package deploy embeds the quadlet unit files the installer renders for the isolated tier.
package deploy

import "embed"

// Quadlet holds the unit files of deploy/quadlet under the prefix "quadlet/".
//
//go:embed quadlet/*
var Quadlet embed.FS
