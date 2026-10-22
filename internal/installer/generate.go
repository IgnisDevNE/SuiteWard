package installer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewPassword returns a database password: 24 random bytes as lowercase hex.
func NewPassword() (string, error) { return randomHex(24) }

// NewMCPToken returns an MCP bearer token: 32 random bytes as lowercase hex.
func NewMCPToken() (string, error) { return randomHex(32) }

// DatabaseURL is the URL of the stack's database for user. sslmode=disable: the database listens only on the private suiteward network.
func DatabaseURL(user, password string) string {
	return "postgres://" + user + ":" + password + "@suiteward-db:5432/suiteward?sslmode=disable"
}
