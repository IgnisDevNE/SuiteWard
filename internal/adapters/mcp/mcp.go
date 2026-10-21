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
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxBodyBytes is the largest request body the handler reads.
const MaxBodyBytes = 64 << 10

// minTokenBytes is the shortest accepted token (MCP_TOKEN).
const minTokenBytes = 32

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

// StatusView is what SuiteWard observed: the effective installation Tier (with
// a TierNote when it differs from the configured one), the Owner once setup
// has run and, without GitHub settings, GitHubConfigured false and no
// Repositories.
type StatusView struct {
	Tier             string             `json:"tier"`
	TierNote         string             `json:"tierNote,omitempty"`
	Instance         string             `json:"instance"`
	Budget           *BudgetStatus      `json:"budget,omitempty"`
	Owner            *OwnerStatus       `json:"owner,omitempty"`
	GitHubConfigured bool               `json:"githubConfigured"`
	Repositories     []RepositoryStatus `json:"repositories"`
	// UnconfiguredRepositories lists the enrolled repositories that GITHUB_REPOSITORIES does not name: SuiteWard
	// skips them until the setting names them again or setup is run without them.
	UnconfiguredRepositories []string `json:"unconfiguredRepositories,omitempty"`
}

// BudgetStatus is the GitHub API budget of the current rate-limit window, shown only while known: Remaining is
// what SuiteWard may still request, Reset when the window ends and requests resume.
type BudgetStatus struct {
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
}

// OwnerStatus is the enrolled owner: the login is display only, the account
// id is the identity.
type OwnerStatus struct {
	Login      string    `json:"login"`
	AccountID  int64     `json:"accountId"`
	EnrolledAt time.Time `json:"enrolledAt"`
}

// RepositoryStatus is one configured repository; a zero time is absent.
type RepositoryStatus struct {
	Repo        string            `json:"repo"`
	LastSuccess time.Time         `json:"lastSuccess,omitzero"`
	LastError   string            `json:"lastError,omitempty"`
	WaitUntil   time.Time         `json:"waitUntil,omitzero"`
	Pulls       []Pull            `json:"pulls"`
	Protection  *ProtectionStatus `json:"protection,omitempty"` // absent before the first verification
}

// ProtectionStatus is the latest protection verification of a repository
// (D-PROTECTION): the result, its finding and caveat codes and when it was
// checked. It is status, not authority.
type ProtectionStatus struct {
	Result    string    `json:"result"`
	Findings  []string  `json:"findings"`
	Caveats   []string  `json:"caveats"`
	CheckedAt time.Time `json:"checkedAt"`
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

// errBackend is the tool error for any other backend failure.
var errBackend = errors.New("mcp: the request failed; see the service log")

// NewHandler returns the MCP handler. backend must not be nil, token must
// hold at least 32 bytes, and every allowed origin must be an absolute
// scheme://host[:port]; origins match case-insensitively.
func NewHandler(backend Backend, token []byte, allowedOrigins []string, version string) (http.Handler, error) {
	if backend == nil {
		return nil, fmt.Errorf("%w: nil backend", ErrInvalidArgument)
	}
	if len(token) < minTokenBytes {
		return nil, fmt.Errorf("%w: token shorter than %d bytes", ErrInvalidArgument, minTokenBytes)
	}
	origins := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if !validOrigin(origin) {
			return nil, fmt.Errorf("%w: origin %q is not scheme://host[:port]", ErrInvalidArgument, origin)
		}
		origins[strings.ToLower(origin)] = true
	}

	server := sdk.NewServer(&sdk.Implementation{Name: "suiteward", Version: version}, nil)
	sdk.AddTool(server, &sdk.Tool{
		Name:        "status",
		Description: "What SuiteWard observed: per configured repository its last synchronization and open pull requests. Not authority.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, StatusView, error) {
		view, err := backend.Status(ctx)
		if err != nil {
			return nil, StatusView{}, errBackend
		}
		return nil, view, nil
	})
	sdk.AddTool(server, &sdk.Tool{
		Name:        "sync",
		Description: "Ask SuiteWard to synchronize one configured repository now; it does not wait for the cycle.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args syncArgs) (*sdk.CallToolResult, SyncResult, error) {
		result, err := backend.RequestSync(ctx, args.Repo)
		switch {
		case err == nil:
			return nil, result, nil
		case errors.Is(err, ErrUnknownRepository):
			return nil, SyncResult{}, ErrUnknownRepository
		case errors.Is(err, ErrNotConfigured):
			return nil, SyncResult{}, ErrNotConfigured
		default:
			return nil, SyncResult{}, errBackend
		}
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: MaxBodyBytes,
	}))
	return &handler{next: mux, token: bytes.Clone(token), origins: origins}, nil
}

type syncArgs struct {
	Repo string `json:"repo" jsonschema:"a configured repository, owner/name"`
}

// validOrigin reports whether origin is exactly scheme://host[:port].
func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme != "" && u.Hostname() != "" && strings.EqualFold(origin, u.Scheme+"://"+u.Host)
}

type handler struct {
	next    http.Handler
	token   []byte
	origins map[string]bool
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if values, present := r.Header["Origin"]; present && (len(values) != 1 || !h.origins[strings.ToLower(values[0])]) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	scheme, credential, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(credential), h.token) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	if r.ContentLength > MaxBodyBytes {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	h.next.ServeHTTP(w, r)
}
