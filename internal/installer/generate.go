package installer

// NewPassword returns a database password: 24 random bytes as lowercase hex.
func NewPassword() (string, error) { return "", errNotImplemented }

// NewMCPToken returns an MCP bearer token: 32 random bytes as lowercase hex.
func NewMCPToken() (string, error) { return "", errNotImplemented }

// DatabaseURL is the URL of the stack's database for user. sslmode=disable: the database listens only on the private suiteward network.
func DatabaseURL(user, password string) string { return "" }
