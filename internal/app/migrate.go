package app

import "time"

// How long migrate waits for the database to accept the owner connection: attempts, and the wait between them.
var (
	connectAttempts = 30
	connectInterval = time.Second
)
