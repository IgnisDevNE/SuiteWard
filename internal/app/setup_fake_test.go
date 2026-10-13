package app

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

const (
	setupAppID       = int64(1)
	setupOwnerID     = int64(884695)
	setupRepoID      = int64(10)
	setupInstallID   = int64(42)
	setupWidgetsRepo = "acme/widgets"
	setupReadToken   = "ghs_FAKE-READ-TOKEN-VALUE"
	setupProtToken   = "ghs_FAKE-PROTECTION-TOKEN-VALUE"
	setupGitHubBody  = "SECRET-BODY-GITHUB-SENT-WITH-AN-ERROR"
	setupTestCheck   = "ci / test"
)

var setupNow = time.Date(2026, 10, 13, 12, 0, 10, 0, time.UTC)

// setupKey is one App key for every test: generating RSA keys is slow.
var setupKey = sync.OnceValue(func() string {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}))
})

// fixedSecret is the TOTP secret setup generates from fixedRandom.
var fixedSecret = bytes.Repeat([]byte{0x5A}, totp.SecretBytes)

// fixedRandom yields the bytes fixedSecret is made of.
func fixedRandom() *bytes.Reader { return bytes.NewReader(fixedSecret) }

// setupGitHub is the GitHub REST API of one installation with the repository
// acme/widgets and the user octo. Its token requests echo the permissions asked for.
type setupGitHub struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenRequests []map[string]any
	// requests are "METHOD path Authorization".
	requests []string

	userType       string // "User" unless set
	userStatus     int    // answer to /users/octo, 200 unless set
	repoStatus     int    // answer to /repos/acme/widgets, 200 unless set
	private        bool
	rules          string // body of the effective rules, `[]` unless set
	rulesStatus    int    // 200 unless set
	classic        string // body of the classic protection; 404 unless set
	protectionMint int    // status of the token request that asks for administration; 0 grants it
	mismatch       bool   // the protection token is granted other permissions than the requested ones
}

func newSetupGitHub(t *testing.T) *setupGitHub {
	t.Helper()
	f := &setupGitHub{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *setupGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	fail := func(status int) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(setupGitHubBody)) // a failed write only fails the test that reads it
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", setupInstallID):
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.tokenRequests = append(f.tokenRequests, raw)
		permissions, _ := json.Marshal(raw["permissions"])
		_ = json.Unmarshal(permissions, &body.Permissions) // the request is checked by the tests through tokenRequests
		granted, token := body.Permissions, setupReadToken
		if _, administration := body.Permissions["administration"]; administration {
			token = setupProtToken
			if f.protectionMint != 0 {
				fail(f.protectionMint)
				return
			}
			if f.mismatch {
				granted = map[string]string{"metadata": "read"}
			}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": setupNow.Add(2 * time.Hour).Format(time.RFC3339), "permissions": granted})
	case r.URL.Path == "/users/octo":
		if f.userStatus != 0 {
			fail(f.userStatus)
			return
		}
		userType := f.userType
		if userType == "" {
			userType = "User"
		}
		_, _ = fmt.Fprintf(w, `{"id":%d,"login":"octo","type":%q}`, setupOwnerID, userType)
	case r.URL.Path == "/repos/"+setupWidgetsRepo:
		if f.repoStatus != 0 {
			fail(f.repoStatus)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%d,"full_name":"acme/widgets","private":%v,"default_branch":"main"}`, setupRepoID, f.private)
	case r.URL.Path == "/repos/"+setupWidgetsRepo+"/rules/branches/main":
		if f.rulesStatus != 0 {
			fail(f.rulesStatus)
			return
		}
		rules := f.rules
		if rules == "" {
			rules = `[]`
		}
		_, _ = w.Write([]byte(rules))
	case r.URL.Path == "/repos/"+setupWidgetsRepo+"/branches/main/protection":
		if f.classic == "" {
			fail(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(f.classic))
	case strings.HasSuffix(r.URL.Path, "/pulls"), strings.HasSuffix(r.URL.Path, "/comments"):
		_, _ = w.Write([]byte(`[]`)) // serve, in the integration test, synchronizes nothing
	default:
		http.NotFound(w, r)
	}
}

// settings are the GitHub settings of a service over f.
func (f *setupGitHub) settings() config.GitHub {
	return config.GitHub{
		AppID: setupAppID, AppKey: config.Secret(setupKey()), InstallationID: setupInstallID,
		Repositories: []string{setupWidgetsRepo}, APIURL: f.server.URL,
	}
}

// vars are the GitHub settings as environment variables.
func (f *setupGitHub) vars() map[string]string {
	return map[string]string{
		"SUITEWARD_GITHUB_APP_ID": "1", "SUITEWARD_GITHUB_APP_KEY": setupKey(), "SUITEWARD_GITHUB_INSTALLATION_ID": "42",
		"SUITEWARD_GITHUB_REPOSITORIES": setupWidgetsRepo, "SUITEWARD_GITHUB_API_URL": f.server.URL,
	}
}

func (f *setupGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// requestedWith reports whether path was requested with the token.
func (f *setupGitHub) requestedWith(path, token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.requests, func(r string) bool { return strings.Contains(r, " "+path+" ") && strings.HasSuffix(r, "Bearer "+token) })
}
