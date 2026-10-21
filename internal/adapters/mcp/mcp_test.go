package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/mcp"
)

// testToken is a fixture, not a credential.
const testToken = "test-token-0123456789-abcdefghijklmnop"

// internalDetail marks a backend failure's text; it must never reach a client.
const internalDetail = "pq: connection to 10.0.0.7 refused"

// fakeBackend answers from fixed values and records the sync requests.
type fakeBackend struct {
	mu        sync.Mutex
	view      mcp.StatusView
	statusErr error
	results   map[string]mcp.SyncResult
	syncErr   error
	calls     int
	requested []string
}

func (f *fakeBackend) Status(context.Context) (mcp.StatusView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.view, f.statusErr
}

func (f *fakeBackend) RequestSync(_ context.Context, repo string) (mcp.SyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requested = append(f.requested, repo)
	if f.syncErr != nil {
		return mcp.SyncResult{}, f.syncErr
	}
	result, ok := f.results[repo]
	if !ok {
		return mcp.SyncResult{}, fmt.Errorf("%w: %s", mcp.ErrUnknownRepository, repo)
	}
	return result, nil
}

func (f *fakeBackend) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requested)
}

func (f *fakeBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// logBuffer collects log output written from several goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newServer serves a handler over the fake backend; the server's error log
// goes to logs.
func newServer(t *testing.T, backend mcp.Backend, logs io.Writer, origins ...string) *httptest.Server {
	t.Helper()
	handler, err := mcp.NewHandler(backend, []byte(testToken), origins, "test-version")
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	if logs == nil {
		logs = io.Discard
	}
	server.Config.ErrorLog = log.New(logs, "", 0)
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// bearer adds the Authorization header to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(req)
}

// connect opens an SDK client session with the test token.
func connect(t *testing.T, server *httptest.Server) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "suiteward-test", Version: "1"}, nil)
	transport := &sdk.StreamableClientTransport{
		Endpoint:             server.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token: testToken, next: server.Client().Transport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return result
}

// decodeResult checks that a successful result carries the value both as
// structured content and as exactly one JSON text item, and returns it.
func decodeResult[T any](t *testing.T, result *sdk.CallToolResult) T {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(t, result))
	}
	var fromText, fromStructured T
	if err := json.Unmarshal([]byte(resultText(t, result)), &fromText); err != nil {
		t.Fatalf("text item is not JSON: %v", err)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(structured, &fromStructured); err != nil {
		t.Fatalf("structured content does not decode: %v", err)
	}
	if !reflect.DeepEqual(fromText, fromStructured) {
		t.Fatalf("text item %+v differs from structured content %+v", fromText, fromStructured)
	}
	return fromText
}

func resultText(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content item is %T, want text", result.Content[0])
	}
	return text.Text
}

func toolError(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatalf("want a tool error, got %s", resultText(t, result))
	}
	return resultText(t, result)
}

func at(hour int) time.Time { return time.Date(2026, 10, 11, hour, 0, 0, 0, time.UTC) }

func configuredBackend() *fakeBackend {
	return &fakeBackend{
		view: mcp.StatusView{
			Tier:             "co-located",
			TierNote:         "note",
			Instance:         "1a2b3c4d",
			Budget:           &mcp.BudgetStatus{Remaining: 120, Reset: at(11)},
			Owner:            &mcp.OwnerStatus{Login: "octo", AccountID: 884695, EnrolledAt: at(6)},
			GitHubConfigured: true,
			Repositories: []mcp.RepositoryStatus{
				{
					Repo:        "acme/api",
					LastSuccess: at(9),
					Pulls: []mcp.Pull{
						{Number: 7, HeadSHA: strings.Repeat("a", 40), BaseRef: "main", UpdatedAt: at(8)},
						{Number: 9, HeadSHA: strings.Repeat("b", 40), BaseRef: "release", UpdatedAt: at(7), NotOpenSince: at(9)},
					},
				},
				{Repo: "acme/web", LastError: "rate limited", WaitUntil: at(10), Pulls: []mcp.Pull{}},
			},
		},
		results: map[string]mcp.SyncResult{
			"acme/api":  {Queued: true},
			"acme/web":  {Reason: mcp.ReasonWaiting, WaitUntil: at(10)},
			"acme/docs": {Reason: mcp.ReasonAlreadyPending},
		},
	}
}

func TestNewHandlerRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	valid := []byte(testToken)
	cases := []struct {
		name    string
		backend mcp.Backend
		token   []byte
		origins []string
	}{
		{"nil backend", nil, valid, nil},
		{"nil token", &fakeBackend{}, nil, nil},
		{"token of 31 bytes", &fakeBackend{}, []byte(strings.Repeat("x", 31)), nil},
		{"empty origin", &fakeBackend{}, valid, []string{""}},
		{"origin without scheme", &fakeBackend{}, valid, []string{"example.com"}},
		{"origin without host", &fakeBackend{}, valid, []string{"https://"}},
		{"origin with slash", &fakeBackend{}, valid, []string{"https://example.com/"}},
		{"origin with path", &fakeBackend{}, valid, []string{"https://example.com/app"}},
		{"origin with query", &fakeBackend{}, valid, []string{"https://example.com?x=1"}},
		{"origin with fragment", &fakeBackend{}, valid, []string{"https://example.com#x"}},
		{"origin with user", &fakeBackend{}, valid, []string{"https://user@example.com"}},
		{"origin with bad port", &fakeBackend{}, valid, []string{"https://example.com:http"}},
		{"wildcard origin", &fakeBackend{}, valid, []string{"*"}},
		{"null origin", &fakeBackend{}, valid, []string{"null"}},
		{"one bad origin among good", &fakeBackend{}, valid, []string{"https://example.com", "example.org"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler, err := mcp.NewHandler(tc.backend, tc.token, tc.origins, "v")
			if !errors.Is(err, mcp.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if handler != nil {
				t.Fatal("handler returned with an error")
			}
		})
	}
}

func TestNewHandlerAcceptsValidArguments(t *testing.T) {
	t.Parallel()
	origins := []string{"https://example.com", "http://localhost:3000", "http://127.0.0.1:8090"}
	handler, err := mcp.NewHandler(&fakeBackend{}, []byte(strings.Repeat("x", 32)), origins, "v")
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if handler == nil {
		t.Fatal("no handler")
	}
}

func TestToolsListReturnsExactlyStatusAndSync(t *testing.T) {
	t.Parallel()
	session := connect(t, newServer(t, configuredBackend(), nil))
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	schemas := map[string]map[string]any{}
	for _, tool := range listed.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema of %s: %v", tool.Name, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("schema of %s: %v", tool.Name, err)
		}
		schemas[tool.Name] = schema
	}
	names := slices.Sorted(maps.Keys(schemas))
	if !slices.Equal(names, []string{"status", "sync"}) || len(listed.Tools) != 2 {
		t.Fatalf("tools = %v, want exactly [status sync]", names)
	}

	status := schemas["status"]
	if status["type"] != "object" || len(asMap(status["properties"])) != 0 || len(asSlice(status["required"])) != 0 {
		t.Fatalf("status input schema = %v, want an object without arguments", status)
	}
	syncSchema := schemas["sync"]
	repo := asMap(asMap(syncSchema["properties"])["repo"])
	if syncSchema["type"] != "object" || len(asMap(syncSchema["properties"])) != 1 || repo["type"] != "string" {
		t.Fatalf("sync input schema = %v, want one string property repo", syncSchema)
	}
	if !reflect.DeepEqual(asSlice(syncSchema["required"]), []any{"repo"}) {
		t.Fatalf("sync required = %v, want [repo]", syncSchema["required"])
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func TestSDKClientNegotiatesTheLatestRevision(t *testing.T) {
	t.Parallel()
	session := connect(t, newServer(t, configuredBackend(), nil))
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocol version = %q, want 2026-07-28", got)
	}
	if got := session.InitializeResult().ServerInfo; got == nil || got.Name != "suiteward" || got.Version != "test-version" {
		t.Fatalf("server info = %+v, want suiteward test-version", got)
	}
}

func TestStatusReturnsTheBackendView(t *testing.T) {
	t.Parallel()
	cases := map[string]*fakeBackend{
		"configured":     configuredBackend(),
		"not configured": {view: mcp.StatusView{Tier: "co-located", GitHubConfigured: false, Repositories: []mcp.RepositoryStatus{}}},
	}
	for name, backend := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			session := connect(t, newServer(t, backend, nil))
			got := decodeResult[mcp.StatusView](t, callTool(t, session, "status", map[string]any{}))
			if !reflect.DeepEqual(got, backend.view) {
				t.Fatalf("status = %+v, want %+v", got, backend.view)
			}
		})
	}
}

func TestStatusJSONUsesTheContractNames(t *testing.T) {
	t.Parallel()
	session := connect(t, newServer(t, configuredBackend(), nil))
	text := resultText(t, callTool(t, session, "status", map[string]any{}))
	for _, field := range []string{
		`"tier":"co-located"`, `"tierNote":"note"`, `"instance":"1a2b3c4d"`, `"budget":{"remaining":120,"reset":"2026-10-11T11:00:00Z"}`, `"login":"octo"`, `"accountId":884695`, `"enrolledAt":"2026-10-11T06:00:00Z"`, `"githubConfigured":true`, `"repo":"acme/api"`, `"lastSuccess":"2026-10-11T09:00:00Z"`,
		`"lastError":"rate limited"`, `"waitUntil":"2026-10-11T10:00:00Z"`, `"number":9`,
		`"headSha":"` + strings.Repeat("b", 40) + `"`, `"baseRef":"release"`,
		`"updatedAt":"2026-10-11T07:00:00Z"`, `"notOpenSince":"2026-10-11T09:00:00Z"`,
	} {
		if !strings.Contains(text, field) {
			t.Errorf("status JSON lacks %s: %s", field, text)
		}
	}
}

func TestSyncReturnsTheBackendResult(t *testing.T) {
	t.Parallel()
	backend := configuredBackend()
	session := connect(t, newServer(t, backend, nil))
	for _, repo := range []string{"acme/api", "acme/docs", "acme/web"} {
		got := decodeResult[mcp.SyncResult](t, callTool(t, session, "sync", map[string]any{"repo": repo}))
		if want := backend.results[repo]; !reflect.DeepEqual(got, want) {
			t.Errorf("sync %s = %+v, want %+v", repo, got, want)
		}
	}
	if got := backend.requests(); !slices.Equal(got, []string{"acme/api", "acme/docs", "acme/web"}) {
		t.Fatalf("backend saw %v", got)
	}
}

func TestSyncUnknownRepositoryAndNotConfiguredAreToolErrors(t *testing.T) {
	t.Parallel()
	t.Run("unknown repository", func(t *testing.T) {
		t.Parallel()
		session := connect(t, newServer(t, configuredBackend(), nil))
		text := toolError(t, callTool(t, session, "sync", map[string]any{"repo": "evil/other"}))
		if text != mcp.ErrUnknownRepository.Error() {
			t.Fatalf("message = %q, want %q", text, mcp.ErrUnknownRepository.Error())
		}
	})
	t.Run("not configured", func(t *testing.T) {
		t.Parallel()
		backend := &fakeBackend{syncErr: fmt.Errorf("compose: %w", mcp.ErrNotConfigured)}
		session := connect(t, newServer(t, backend, nil))
		text := toolError(t, callTool(t, session, "sync", map[string]any{"repo": "acme/api"}))
		if text != mcp.ErrNotConfigured.Error() {
			t.Fatalf("message = %q, want %q", text, mcp.ErrNotConfigured.Error())
		}
	})
	t.Run("missing repo argument", func(t *testing.T) {
		t.Parallel()
		backend := configuredBackend()
		session := connect(t, newServer(t, backend, nil))
		toolError(t, callTool(t, session, "sync", map[string]any{}))
		if backend.callCount() != 0 {
			t.Fatal("backend called without a repo")
		}
	})
}

func TestBackendFailuresAreFixedToolErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New(internalDetail)
	backend := &fakeBackend{statusErr: failure, syncErr: failure}
	session := connect(t, newServer(t, backend, nil))
	for _, call := range []struct {
		tool string
		args map[string]any
	}{{"status", map[string]any{}}, {"sync", map[string]any{"repo": "acme/api"}}} {
		text := toolError(t, callTool(t, session, call.tool, call.args))
		if text == "" || strings.Contains(text, internalDetail) || strings.Contains(text, "10.0.0.7") {
			t.Errorf("%s error = %q, want a fixed message without the internal detail", call.tool, text)
		}
	}
}

// post sends a raw POST to /mcp with the MCP content headers plus extra.
func post(t *testing.T, server *httptest.Server, body io.Reader, extra map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range extra {
		req.Header.Set(key, value)
	}
	return send(t, server, req)
}

func send(t *testing.T, server *httptest.Server, req *http.Request) (int, http.Header, string) {
	t.Helper()
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

const legacyInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`

func legacyHeaders(extra map[string]string) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + testToken, "MCP-Protocol-Version": "2025-06-18"}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

func TestRequestsWithoutAValidTokenAreUnauthorized(t *testing.T) {
	t.Parallel()
	backend := configuredBackend()
	server := newServer(t, backend, nil)
	cases := map[string]string{
		"no token":           "",
		"wrong token":        "Bearer " + strings.Replace(testToken, "test", "best", 1),
		"token prefix":       "Bearer " + testToken[:32],
		"token with suffix":  "Bearer " + testToken + "x",
		"basic scheme":       "Basic " + testToken,
		"token alone":        testToken,
		"bearer empty token": "Bearer ",
	}
	for name, authorization := range cases {
		extra := map[string]string{"MCP-Protocol-Version": "2025-06-18"}
		if authorization != "" {
			extra["Authorization"] = authorization
		}
		status, _, body := post(t, server, strings.NewReader(legacyInitialize), extra)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, status)
		}
		if strings.Contains(body, testToken[:16]) || strings.Contains(body, "Bearer") {
			t.Errorf("%s: body carries detail: %q", name, body)
		}
	}
	if backend.callCount() != 0 {
		t.Fatal("backend called without authentication")
	}
}

func TestOriginCheck(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil, "https://agent.example.com", "http://localhost:3000")
	cases := []struct {
		name   string
		origin string
		token  bool
		want   int
	}{
		{"not allowed with token", "https://evil.example.com", true, http.StatusForbidden},
		{"not allowed without token", "https://evil.example.com", false, http.StatusForbidden},
		{"allowed origin with other port", "http://localhost:3001", true, http.StatusForbidden},
		{"null origin", "null", true, http.StatusForbidden},
		{"allowed", "https://agent.example.com", true, http.StatusOK},
		{"allowed in another case", "HTTPS://Agent.Example.com", true, http.StatusOK},
		{"second allowed", "http://localhost:3000", true, http.StatusOK},
		{"no origin", "", true, http.StatusOK},
	}
	for _, tc := range cases {
		extra := map[string]string{"MCP-Protocol-Version": "2025-06-18"}
		if tc.token {
			extra["Authorization"] = "Bearer " + testToken
		}
		if tc.origin != "" {
			extra["Origin"] = tc.origin
		}
		status, _, body := post(t, server, strings.NewReader(legacyInitialize), extra)
		if status != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, status, tc.want, body)
		}
	}
}

func TestEmptyOriginHeaderIsNotAllowed(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil, "https://agent.example.com")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", strings.NewReader(legacyInitialize))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header["Origin"] = []string{""}
	if status, _, _ := send(t, server, req); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

// paddedToolsList is a valid tools/list request of exactly size bytes.
func paddedToolsList(t *testing.T, size int) string {
	t.Helper()
	request := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	if size < len(request)+1 {
		t.Fatalf("size %d too small", size)
	}
	return request[:len(request)-1] + strings.Repeat(" ", size-len(request)) + "}"
}

func TestBodyLimit(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil)
	status, _, body := post(t, server, strings.NewReader(paddedToolsList(t, mcp.MaxBodyBytes)), legacyHeaders(nil))
	if status != http.StatusOK || !strings.Contains(body, `"sync"`) {
		t.Fatalf("body of exactly %d bytes: status %d, %s", mcp.MaxBodyBytes, status, body)
	}
	if mcp.MaxBodyBytes != 64<<10 {
		t.Fatalf("MaxBodyBytes = %d, want 64 KiB", mcp.MaxBodyBytes)
	}
	over := paddedToolsList(t, mcp.MaxBodyBytes+1)
	if status, _, _ := post(t, server, strings.NewReader(over), legacyHeaders(nil)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("known length above the limit: status = %d, want 413", status)
	}
	// A reader without a known length is sent chunked.
	chunked := io.MultiReader(strings.NewReader(over))
	if status, _, _ := post(t, server, chunked, legacyHeaders(nil)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked above the limit: status = %d, want 413", status)
	}
}

func TestLegacyClientIsServed(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil)
	status, _, body := post(t, server, strings.NewReader(legacyInitialize), legacyHeaders(nil))
	if status != http.StatusOK {
		t.Fatalf("initialize: status %d, %s", status, body)
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &initialized); err != nil {
		t.Fatalf("initialize answer %q: %v", body, err)
	}
	if initialized.Result.ProtocolVersion != "2025-06-18" || initialized.Result.ServerInfo.Name != "suiteward" {
		t.Fatalf("initialize result = %+v", initialized.Result)
	}

	notification := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if status, _, body := post(t, server, strings.NewReader(notification), legacyHeaders(nil)); status != http.StatusAccepted {
		t.Fatalf("initialized notification: status %d, %s", status, body)
	}

	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	status, header, body := post(t, server, strings.NewReader(list), legacyHeaders(nil))
	if status != http.StatusOK || !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("tools/list: status %d, content type %q, %s", status, header.Get("Content-Type"), body)
	}
	if !strings.Contains(body, `"name":"status"`) || !strings.Contains(body, `"name":"sync"`) {
		t.Fatalf("tools/list answer lacks the tools: %s", body)
	}
}

const modernStatusCall = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{},` +
	`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientInfo":{"name":"raw","version":"1"},` +
	`"io.modelcontextprotocol/clientCapabilities":{}}}}`

func TestModernRequestIsServed(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil)
	headers := map[string]string{
		"Authorization":        "Bearer " + testToken,
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "status",
	}
	status, header, body := post(t, server, strings.NewReader(modernStatusCall), headers)
	if status != http.StatusOK || !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q, %s", status, header.Get("Content-Type"), body)
	}
	if !strings.Contains(body, `"githubConfigured":true`) || strings.Contains(body, `"isError":true`) {
		t.Fatalf("answer lacks the status view: %s", body)
	}

	// The SDK validates the request-metadata headers of this revision.
	delete(headers, "Mcp-Method")
	if status, _, body := post(t, server, strings.NewReader(modernStatusCall), headers); status != http.StatusBadRequest {
		t.Fatalf("without Mcp-Method: status %d, want 400 (%s)", status, body)
	}
}

// TestGetAndDeleteDoNotOpenAStream records the SDK's stateless answer: 405
// with Allow: POST.
func TestGetAndDeleteDoNotOpenAStream(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/mcp", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		status, header, _ := send(t, server, req)
		if status != http.StatusMethodNotAllowed || header.Get("Allow") != http.MethodPost {
			t.Errorf("%s: status %d, Allow %q; want 405 with Allow POST", method, status, header.Get("Allow"))
		}
		if strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
			t.Errorf("%s opened an event stream", method)
		}
	}
}

// TestTheTokenIsCheckedBeforeMethodAndPath locks the middleware order that
// deploy/smoke.sh relies on: a request without the token is 401 whatever its
// method or path, never the SDK's 405 or the mux's 404.
func TestTheTokenIsCheckedBeforeMethodAndPath(t *testing.T) {
	t.Parallel()
	backend := configuredBackend()
	server := newServer(t, backend, nil)
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/mcp"},
		{http.MethodDelete, "/mcp"},
		{http.MethodPost, "/other"},
		{http.MethodGet, "/"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), request.method, server.URL+request.path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if status, _, _ := send(t, server, req); status != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: status %d, want 401", request.method, request.path, status)
		}
	}
	if backend.callCount() != 0 {
		t.Fatal("backend called without authentication")
	}
}

func TestOnlyTheMCPPathIsServed(t *testing.T) {
	t.Parallel()
	server := newServer(t, configuredBackend(), nil)
	for _, path := range []string{"/", "/mcp/", "/mcp/x", "/status"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(legacyInitialize))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+testToken)
		if status, _, _ := send(t, server, req); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, status)
		}
	}
}

// TestTokenIsNeverLoggedOrReturned is not parallel: it captures the default
// slog logger, and the package's parallel tests run only after it.
func TestTokenIsNeverLoggedOrReturned(t *testing.T) {
	var logs logBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	backend := configuredBackend()
	backend.statusErr = errors.New(internalDetail + " " + testToken)
	server := newServer(t, backend, &logs, "https://agent.example.com")
	var bodies []string
	record := func(want int, status int, body string) {
		t.Helper()
		if status != want {
			t.Fatalf("status = %d, want %d (%s)", status, want, body)
		}
		bodies = append(bodies, body)
	}

	status, _, body := post(t, server, strings.NewReader(legacyInitialize), legacyHeaders(nil))
	record(http.StatusOK, status, body)
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	status, _, body = post(t, server, strings.NewReader(list), legacyHeaders(nil))
	record(http.StatusOK, status, body)
	call := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"status","arguments":{}}}`
	status, _, body = post(t, server, strings.NewReader(call), legacyHeaders(nil))
	record(http.StatusOK, status, body)
	if !strings.Contains(body, `"isError":true`) {
		t.Fatalf("failing status is not a tool error: %s", body)
	}
	status, _, body = post(t, server, strings.NewReader(legacyInitialize), legacyHeaders(map[string]string{"Authorization": "Bearer " + testToken + "x"}))
	record(http.StatusUnauthorized, status, body)
	status, _, body = post(t, server, strings.NewReader(legacyInitialize), legacyHeaders(map[string]string{"Origin": "https://evil.example.com"}))
	record(http.StatusForbidden, status, body)
	status, _, body = post(t, server, strings.NewReader(paddedToolsList(t, mcp.MaxBodyBytes+1)), legacyHeaders(nil))
	record(http.StatusRequestEntityTooLarge, status, body)

	session := connect(t, server)
	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	server.Close()

	for _, secret := range []string{testToken, "Bearer " + testToken, internalDetail} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs carry %q: %s", secret, logs.String())
		}
		for _, body := range bodies {
			if strings.Contains(body, secret) {
				t.Errorf("response carries %q: %s", secret, body)
			}
		}
	}
}

func TestStatusOmitsTheOwnerAndTheNoteWhenThereIsNone(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{view: mcp.StatusView{Tier: "co-located", Repositories: []mcp.RepositoryStatus{}}}
	text := resultText(t, callTool(t, connect(t, newServer(t, backend, nil)), "status", map[string]any{}))
	for _, field := range []string{`"owner"`, `"tierNote"`, `"budget"`} {
		if strings.Contains(text, field) {
			t.Errorf("status JSON has %s without an enrollment: %s", field, text)
		}
	}
}
