// Package mcp serves SuiteWard's local MCP endpoint (docs/contracts/runtime.md,
// "Local MCP"; D-SYNC): the tools status and sync, nothing else.
//
// NewHandler returns an http.Handler for POST /mcp. The protocol is the
// official Go SDK's Streamable HTTP handler in stateless mode with JSON
// responses, so the SDK negotiates the revision (2026-07-28 and the earlier
// initialize-based ones), answers server/discover and validates the request
// metadata headers. Before the SDK sees a request, the handler checks, in
// this order: the Origin header (403 unless allowed), the bearer token (401,
// compared in constant time) and the body size (413 above MaxBodyBytes).
//
// The tools call a Backend, implemented by the composition. status returns
// the backend's StatusView; sync returns its SyncResult. A sync for an
// unknown repository (ErrUnknownRepository) or without GitHub settings
// (ErrNotConfigured) is a tool error carrying that sentinel's message; any
// other backend error is a tool error with a fixed message, so the backend
// logs its own failure detail. The handler logs nothing, and never returns
// the token or the Authorization header.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// MaxBodyBytes is the largest request body the handler reads.
const MaxBodyBytes = 64 << 10

// Reasons a SyncResult is not queued.
const (
	ReasonAlreadyPending = "already_pending"
	ReasonWaiting        = "waiting"
)

var (
	// ErrInvalidArgument reports a NewHandler argument that breaks its rules.
	ErrInvalidArgument = errors.New("mcp: invalid argument")
	// ErrNotConfigured is returned by a Backend's RequestSync when the GitHub
	// settings are unset.
	ErrNotConfigured = errors.New("mcp: GitHub is not configured")
	// ErrUnknownRepository is returned by a Backend's RequestSync for a
	// repository that is not configured.
	ErrUnknownRepository = errors.New("mcp: unknown repository")
)

// Backend is the port the tools call.
type Backend interface {
	Status(ctx context.Context) (StatusView, error)
	RequestSync(ctx context.Context, repo string) (SyncResult, error)
}

// StatusView is what SuiteWard observed. Without GitHub settings
// GitHubConfigured is false and Repositories is empty.
type StatusView struct {
	GitHubConfigured bool               `json:"githubConfigured"`
	Repositories     []RepositoryStatus `json:"repositories"`
}

// RepositoryStatus is one configured repository; a zero time is absent.
type RepositoryStatus struct {
	Repo        string    `json:"repo"`
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	WaitUntil   time.Time `json:"waitUntil,omitzero"`
	Pulls       []Pull    `json:"pulls"`
}

// Pull is one open pull request; it carries no comment bodies.
type Pull struct {
	Number       int       `json:"number"`
	HeadSHA      string    `json:"headSha"`
	BaseRef      string    `json:"baseRef"`
	UpdatedAt    time.Time `json:"updatedAt"`
	NotOpenSince time.Time `json:"notOpenSince,omitzero"`
}

// SyncResult answers a sync request: queued, or not with a Reason
// (ReasonWaiting carries WaitUntil).
type SyncResult struct {
	Queued    bool      `json:"queued"`
	Reason    string    `json:"reason,omitempty"`
	WaitUntil time.Time `json:"waitUntil,omitzero"`
}

var errNotImplemented = errors.New("mcp: not implemented")

// NewHandler returns the MCP handler. backend must not be nil, token must
// hold at least 32 bytes, and every allowed origin must be an absolute
// scheme://host[:port].
func NewHandler(backend Backend, token []byte, allowedOrigins []string, version string) (http.Handler, error) {
	return nil, errNotImplemented
}
