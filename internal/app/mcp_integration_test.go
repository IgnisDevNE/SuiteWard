//go:build integration

package app

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const integrationMCPToken = "integration-mcp-token-0123456789-abcdefghij"

// mcpServeVars adds the MCP settings; the listener binds a free port, which the service logs.
func mcpServeVars(vars map[string]string) {
	vars["SUITEWARD_MCP_TOKEN"] = integrationMCPToken
	vars["SUITEWARD_MCP_ADDR"] = "localhost:0" // not the HTTP_ADDR text, which the settings reject
}

// mcpAddr is the address the MCP listener logged.
func (s *service) mcpAddr() string {
	s.t.Helper()
	entry, _ := s.logs.find("mcp listening")
	if entry == nil {
		s.t.Fatalf("the service did not log its MCP listener:\n%s", s.logs.String())
	}
	addr, _ := entry["addr"].(string)
	return addr
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

// mcpSession connects the SDK client to the real listener with the token.
func (s *service) mcpSession() *sdk.ClientSession {
	s.t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "suiteward-integration", Version: "1"}, nil)
	session, err := client.Connect(s.t.Context(), &sdk.StreamableClientTransport{
		Endpoint:             "http://" + s.mcpAddr() + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{integrationMCPToken}, Timeout: 30 * time.Second},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		s.t.Fatalf("connect the SDK client to the MCP listener: %v\n%s", err, s.logs.String())
	}
	s.t.Cleanup(func() { _ = session.Close() }) // a failed close only means the service is already gone
	return session
}

// callMCP calls a tool and returns the result with its one text item.
func callMCP(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) (result *sdk.CallToolResult, text string) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s returned %d content items; want 1", name, len(result.Content))
	}
	item, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s returned %T; want text", name, result.Content[0])
	}
	return result, item.Text
}

func TestServeAnswersMCPOverTheRealListener(t *testing.T) {
	databaseURL := newDatabase(t)
	fake := newFakeGitHub(t)
	fake.gate = make(chan struct{})
	vars := serveVars(t, databaseURL)
	githubVars(t, vars, fake)
	mcpServeVars(vars)
	s := startRun(t, vars)
	s.waitReady()
	fake.gate <- struct{}{} // the tick that runs at start
	s.waitGitHubStatus("the first cycle", func(e map[string]any) bool { return e["lastSuccess"] != nil })
	session := s.mcpSession()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if len(names) != 2 || !(names[0] == "status" && names[1] == "sync" || names[0] == "sync" && names[1] == "status") {
		t.Fatalf("tools/list = %v; want status and sync", names)
	}

	_, text := callMCP(t, session, "status", nil)
	var view struct {
		GitHubConfigured bool `json:"githubConfigured"`
		Repositories     []struct {
			Repo        string `json:"repo"`
			LastSuccess string `json:"lastSuccess"`
			LastError   string `json:"lastError"`
			Pulls       []struct {
				Number       int    `json:"number"`
				HeadSHA      string `json:"headSha"`
				BaseRef      string `json:"baseRef"`
				NotOpenSince string `json:"notOpenSince"`
			} `json:"pulls"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatalf("status text is not JSON: %v\n%s", err, text)
	}
	if !view.GitHubConfigured || len(view.Repositories) != 1 {
		t.Fatalf("status = %s; want GitHub configured with one repository", text)
	}
	repo := view.Repositories[0]
	if repo.Repo != "acme/widgets" || repo.LastSuccess == "" || repo.LastError != "" || len(repo.Pulls) != 1 ||
		repo.Pulls[0].Number != 1 || repo.Pulls[0].HeadSHA != sha40a || repo.Pulls[0].BaseRef != "main" || repo.Pulls[0].NotOpenSince != "" {
		t.Fatalf("status = %s; want the synchronized repository and its open pull", text)
	}
	if strings.Contains(text, fakeCommentBody) {
		t.Fatalf("status carries a comment body: %s", text)
	}

	// The job waits on the gate, so the second request meets an unfinished one.
	result, text := callMCP(t, session, "sync", map[string]any{"repo": "acme/widgets"})
	if result.IsError || !strings.Contains(text, `"queued":true`) {
		t.Fatalf("first sync = %v %s; want queued", result.IsError, text)
	}
	waitFor(t, "the sync job to reach GitHub", 30*time.Second, func() bool {
		return scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = 'github_sync_repo' AND state = 'running'") == "1"
	})
	result, text = callMCP(t, session, "sync", map[string]any{"repo": "acme/widgets"})
	if result.IsError || !strings.Contains(text, `"queued":false`) || !strings.Contains(text, `"reason":"already_pending"`) {
		t.Fatalf("second sync = %v %s; want already_pending", result.IsError, text)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = 'github_sync_repo'"); got != "1" {
		t.Fatalf("%s sync jobs; want the second request coalesced into the first", got)
	}
	fake.gate <- struct{}{} // let the job finish

	result, text = callMCP(t, session, "sync", map[string]any{"repo": "someone/else"})
	if !result.IsError || !strings.Contains(text, "unknown repository") {
		t.Fatalf("sync of an unknown repository = %v %s; want a tool error", result.IsError, text)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = 'github_sync_repo'"); got != "1" {
		t.Fatalf("%s sync jobs after an unknown repository; want nothing inserted for it", got)
	}

	if code := s.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, s.logs.String())
	}
	for what, secret := range map[string]string{"the MCP token": integrationMCPToken, "a comment body": fakeCommentBody, "the installation token": fakeToken} {
		if strings.Contains(s.logs.String(), secret) {
			t.Errorf("the logs contain %s:\n%s", what, s.logs.String())
		}
	}
}

func TestServeMCPWithoutGitHubSaysNotConfigured(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	mcpServeVars(vars)
	s := startRun(t, vars)
	s.waitReady()
	session := s.mcpSession()

	_, text := callMCP(t, session, "status", nil)
	var view struct {
		GitHubConfigured bool  `json:"githubConfigured"`
		Repositories     []any `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(text), &view); err != nil || view.GitHubConfigured || view.Repositories == nil || len(view.Repositories) != 0 {
		t.Fatalf("status = %s (%v); want GitHub not configured and no repositories", text, err)
	}
	result, text := callMCP(t, session, "sync", map[string]any{"repo": "acme/widgets"})
	if !result.IsError || !strings.Contains(text, "not configured") {
		t.Fatalf("sync = %v %s; want a tool error saying GitHub is not configured", result.IsError, text)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind LIKE 'github%'"); got != "0" {
		t.Fatalf("%s GitHub jobs without GitHub settings", got)
	}
}

func TestServeWithoutAMCPTokenListensOnNoMCPAddress(t *testing.T) {
	s := startRun(t, serveVars(t, newDatabase(t)))
	s.waitReady()

	if entry, _ := s.logs.find("mcp listening"); entry != nil {
		t.Fatalf("serve opened an MCP listener without MCP_TOKEN:\n%s", s.logs.String())
	}
}

func TestShutdownClosesTheMCPListener(t *testing.T) {
	vars := serveVars(t, newDatabase(t))
	mcpServeVars(vars)
	s := startRun(t, vars)
	s.waitReady()
	addr := s.mcpAddr()
	session := s.mcpSession()
	if _, text := callMCP(t, session, "status", nil); !strings.Contains(text, "githubConfigured") {
		t.Fatalf("status = %s", text)
	}

	if code := s.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, s.logs.String())
	}

	if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		_ = conn.Close() // the connection only proved the listener is open
		t.Fatalf("the MCP listener on %s still accepts connections after the shutdown", addr)
	}
	if entry, _ := s.logs.find("mcp server stopped"); entry == nil {
		t.Fatalf("the shutdown did not report the MCP server stopped:\n%s", s.logs.String())
	}
}

func TestMCPListenFailureFailsStartup(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }() // closing the probe listener cannot fail in a way the test cares about
	vars := serveVars(t, newDatabase(t))
	vars["SUITEWARD_MCP_TOKEN"] = integrationMCPToken
	vars["SUITEWARD_MCP_ADDR"] = blocker.Addr().String()

	logs := &logBuffer{}
	code := make(chan int, 1)
	go func() {
		code <- Run(t.Context(), []string{"serve"}, testEnv(vars), logs, io.Discard)
	}()

	select {
	case got := <-code:
		if got != 1 {
			t.Fatalf("serve exited %d; want 1:\n%s", got, logs.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("serve kept running although the MCP address is taken:\n%s", logs.String())
	}
	if entry, _ := logs.find("listening"); entry != nil {
		t.Fatalf("the HTTP listener opened although MCP failed to listen:\n%s", logs.String())
	}
}
