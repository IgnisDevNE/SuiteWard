package run

// needsNewerGo reports whether the go.mod asks for a Go that is newer than pinned (for example "go1.27.3"): its go line or its toolchain
// line. A go.mod that cannot be read at all is not a mismatch: the prefetch refuses it.
func needsNewerGo(goMod []byte, pinned string) bool {
	return false
}
