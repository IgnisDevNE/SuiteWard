// Package config turns an injected environment into validated settings for
// `suiteward serve` and `suiteward probe` (see docs/contracts/runtime.md).
package config

import (
	"errors"
	"log/slog"
	"os"
	"time"
)

// Secret is a setting value that must never be rendered. Reveal returns it.
type Secret string

// Reveal returns the secret value for the code that must use it.
func (s Secret) Reveal() string { return string(s) }

// Config holds every validated setting.
type Config struct {
	DatabaseURL        Secret
	ArtifactDir        string
	HTTPAddr           string
	ShutdownTimeout    time.Duration
	LogLevel           slog.Level
	JobWorkers         int
	JobTimeout         time.Duration
	JobMaxAttempts     int
	OutboxMaxAttempts  int
	OutboxPollInterval time.Duration
	OutboxLease        time.Duration
}

// Load validates the environment; see the settings table in the runtime contract.
func Load(lookup func(string) (string, bool), environ []string, readFile func(string) ([]byte, error)) (Config, []string, error) {
	return Config{}, nil, errors.New("not implemented")
}

// OS wires the real process environment and filesystem for main: config.Load(config.OS()).
func OS() (func(string) (string, bool), []string, func(string) ([]byte, error)) {
	return os.LookupEnv, os.Environ(), os.ReadFile
}
