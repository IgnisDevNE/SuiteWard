package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

const mcpToken = "0123456789abcdef0123456789abcdef-TOPSECRETMCPTOKEN"

func mcpVars(t *testing.T) map[string]string {
	t.Helper()
	vars := required(t)
	vars["SUITEWARD_MCP_TOKEN"] = mcpToken
	return vars
}

func TestLoadMCPIsAbsentWhenUnset(t *testing.T) {
	cfg, warnings, err := fakeEnv{vars: required(t)}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: warnings %v, error %v", warnings, err)
	}
	if cfg.MCP != nil {
		t.Fatalf("MCP = %+v, want it absent without MCP_TOKEN", cfg.MCP)
	}
}

func TestLoadMCPDefaults(t *testing.T) {
	cfg, warnings, err := fakeEnv{vars: mcpVars(t)}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: warnings %v, error %v", warnings, err)
	}
	if cfg.MCP == nil {
		t.Fatal("MCP is absent although MCP_TOKEN is set")
	}
	if cfg.MCP.Addr != "127.0.0.1:8090" || cfg.MCP.Token.Reveal() != mcpToken || len(cfg.MCP.AllowedOrigins) != 0 {
		t.Fatalf("MCP = %+v, want the default address, the token and no origins", cfg.MCP)
	}
}

func TestLoadMCPReadsEverySetting(t *testing.T) {
	vars := mcpVars(t)
	delete(vars, "SUITEWARD_MCP_TOKEN")
	vars["SUITEWARD_MCP_TOKEN_FILE"] = "/t"
	vars["SUITEWARD_MCP_ADDR"] = "0.0.0.0:9191"
	vars["SUITEWARD_MCP_ALLOWED_ORIGINS"] = " http://localhost:3000 , https://app.example.com "
	cfg, warnings, err := fakeEnv{vars: vars, files: map[string]string{"/t": mcpToken + "\n"}}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: warnings %v, error %v", warnings, err)
	}
	if cfg.MCP == nil {
		t.Fatal("MCP is absent")
	}
	if cfg.MCP.Addr != "0.0.0.0:9191" || cfg.MCP.Token.Reveal() != mcpToken ||
		!slices.Equal(cfg.MCP.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.com"}) {
		t.Fatalf("MCP = %+v, want the values given, trimmed", cfg.MCP)
	}
}

func TestLoadMCPToken(t *testing.T) {
	for name, test := range map[string]struct {
		token   string
		wantErr bool
	}{
		"31 bytes":             {strings.Repeat("x", 31), true},
		"32 bytes":             {strings.Repeat("x", 32), false},
		"30 bytes of 15 runes": {strings.Repeat("é", 15), true},
		"32 bytes of 16 runes": {strings.Repeat("é", 16), false},
	} {
		t.Run(name, func(t *testing.T) {
			vars := required(t)
			vars["SUITEWARD_MCP_TOKEN"] = test.token
			_, _, err := fakeEnv{vars: vars}.load()
			if (err != nil) != test.wantErr {
				t.Fatalf("Load = %v, want error %v", err, test.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "SUITEWARD_MCP_TOKEN") || strings.Contains(err.Error(), test.token)) {
				t.Fatalf("error = %v, want it to name MCP_TOKEN without the value", err)
			}
		})
	}
}

func TestLoadMCPTokenFileProblems(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MCP_TOKEN_FILE"] = "/missing"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MCP_TOKEN_FILE") || strings.Count(err.Error(), "SUITEWARD_MCP_TOKEN") != 1 {
		t.Fatalf("Load = %v, want one error naming MCP_TOKEN_FILE", err)
	}
	vars["SUITEWARD_MCP_TOKEN"] = mcpToken
	if _, _, err := (fakeEnv{vars: vars, files: map[string]string{"/missing": mcpToken}}).load(); err == nil || strings.Contains(err.Error(), mcpToken) {
		t.Fatalf("Load = %v, want both forms refused without the token", err)
	}
}

func TestLoadMCPAddr(t *testing.T) {
	for name, test := range map[string]struct {
		addr    string
		http    string
		wantErr bool
	}{
		"host and port":            {"127.0.0.1:9", "", false},
		"all interfaces":           {":9191", "", false},
		"no port":                  {"127.0.0.1", "", true},
		"not an address":           {"nonsense", "", true},
		"equal to HTTP_ADDR":       {"127.0.0.1:8085", "127.0.0.1:8085", true},
		"default equals HTTP_ADDR": {"", "127.0.0.1:8090", true},
		"another port":             {"127.0.0.1:8091", "127.0.0.1:8085", false},
	} {
		t.Run(name, func(t *testing.T) {
			vars := mcpVars(t)
			if test.addr != "" {
				vars["SUITEWARD_MCP_ADDR"] = test.addr
			}
			if test.http != "" {
				vars["SUITEWARD_HTTP_ADDR"] = test.http
			}
			_, _, err := fakeEnv{vars: vars}.load()
			if (err != nil) != test.wantErr {
				t.Fatalf("Load = %v, want error %v", err, test.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "SUITEWARD_MCP_ADDR") {
				t.Fatalf("error = %v, want it to name MCP_ADDR", err)
			}
		})
	}
}

func TestLoadMCPAllowedOrigins(t *testing.T) {
	for name, test := range map[string]struct {
		value string
		want  []string // nil: an error
	}{
		"one":                {"http://localhost:3000", []string{"http://localhost:3000"}},
		"several, trimmed":   {" http://a.example , https://b.example:8443", []string{"http://a.example", "https://b.example:8443"}},
		"no scheme":          {"localhost:3000", nil},
		"relative":           {"/app", nil},
		"a path":             {"http://a.example/app", nil},
		"a trailing slash":   {"http://a.example/", nil},
		"a query":            {"http://a.example?x=1", nil},
		"a fragment":         {"http://a.example#x", nil},
		"user info":          {"http://user:pw@a.example", nil},
		"no host":            {"http://", nil},
		"an empty item":      {"http://a.example,,http://b.example", nil},
		"a trailing comma":   {"http://a.example,", nil},
		"one bad of several": {"http://a.example,nope", nil},
	} {
		t.Run(name, func(t *testing.T) {
			vars := mcpVars(t)
			vars["SUITEWARD_MCP_ALLOWED_ORIGINS"] = test.value
			cfg, _, err := fakeEnv{vars: vars}.load()
			if test.want == nil {
				if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MCP_ALLOWED_ORIGINS") {
					t.Fatalf("Load = %v, want an error naming MCP_ALLOWED_ORIGINS", err)
				}
				if strings.Contains(err.Error(), "pw@") {
					t.Fatalf("error = %v, want user info not echoed", err)
				}
				return
			}
			if err != nil || cfg.MCP == nil || !slices.Equal(cfg.MCP.AllowedOrigins, test.want) {
				t.Fatalf("Load = %+v, %v; want origins %v", cfg.MCP, err, test.want)
			}
		})
	}
}

func TestLoadMCPAddrWithoutATokenIsAcceptedAndListensNowhere(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MCP_ADDR"] = "127.0.0.1:9191"
	cfg, _, err := fakeEnv{vars: vars}.load()
	if err != nil || cfg.MCP != nil {
		t.Fatalf("Load = MCP %+v, %v; want success with no MCP settings", cfg.MCP, err)
	}
}

func TestLoadMCPAddrWithoutATokenIsStillValidated(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MCP_ADDR"] = "nonsense"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MCP_ADDR") || strings.Contains(err.Error(), "SUITEWARD_MCP_TOKEN") {
		t.Fatalf("Load = %v, want an address error that does not blame the missing token", err)
	}
}

func TestLoadMCPAddrMayEqualHTTPAddrWithoutAToken(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_HTTP_ADDR"] = "127.0.0.1:8085"
	vars["SUITEWARD_MCP_ADDR"] = "127.0.0.1:8085"
	cfg, _, err := fakeEnv{vars: vars}.load()
	if err != nil || cfg.MCP != nil {
		t.Fatalf("Load = MCP %+v, %v; want success with no MCP settings", cfg.MCP, err)
	}
}

func TestLoadMCPImageDefaultsWithoutATokenLoad(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_HTTP_ADDR"] = "0.0.0.0:8080"
	vars["SUITEWARD_MCP_ADDR"] = "0.0.0.0:8090"
	cfg, _, err := fakeEnv{vars: vars}.load()
	if err != nil || cfg.MCP != nil {
		t.Fatalf("Load = MCP %+v, %v; want success with no MCP settings", cfg.MCP, err)
	}
}

func TestLoadMCPAllowedOriginsWithoutATokenIsAnError(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MCP_ALLOWED_ORIGINS"] = "http://a.example"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MCP_ALLOWED_ORIGINS") || !strings.Contains(err.Error(), "SUITEWARD_MCP_TOKEN") {
		t.Fatalf("Load = %v, want an error naming both variables", err)
	}
}

func TestLoadMCPRejectsFileFormForNonSecretSettings(t *testing.T) {
	for _, name := range []string{"ADDR", "ALLOWED_ORIGINS"} {
		vars := mcpVars(t)
		vars["SUITEWARD_MCP_"+name+"_FILE"] = "/f"
		_, _, err := fakeEnv{vars: vars}.load()
		if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MCP_"+name+"_FILE") {
			t.Errorf("%s_FILE: error = %v, want it rejected", name, err)
		}
	}
}

func TestLoadMCPReportsEveryProblemAtOnce(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MCP_TOKEN"] = "short"
	vars["SUITEWARD_MCP_ADDR"] = "nonsense"
	vars["SUITEWARD_MCP_ALLOWED_ORIGINS"] = "nope"
	vars["SUITEWARD_JOB_WORKERS"] = "0"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil {
		t.Fatal("Load accepted invalid settings")
	}
	for _, name := range []string{"MCP_TOKEN", "MCP_ADDR", "MCP_ALLOWED_ORIGINS", "JOB_WORKERS"} {
		if !strings.Contains(err.Error(), "SUITEWARD_"+name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "short") {
		t.Errorf("error echoes the token: %v", err)
	}
}

func TestLoadMCPTokenIsNeverRendered(t *testing.T) {
	cfg, _, err := fakeEnv{vars: mcpVars(t)}.load()
	if err != nil || cfg.MCP == nil {
		t.Fatalf("Load = %+v, %v", cfg.MCP, err)
	}
	renderings := map[string]string{
		"%v":      fmt.Sprintf("%v", cfg),
		"%+v":     fmt.Sprintf("%+v", cfg),
		"%#v":     fmt.Sprintf("%#v", cfg),
		"%v mcp":  fmt.Sprintf("%v", *cfg.MCP),
		"%+v mcp": fmt.Sprintf("%+v", *cfg.MCP),
		"%#v mcp": fmt.Sprintf("%#v", *cfg.MCP),
	}
	var jsonOut, textOut bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonOut, nil)).Info("config", "config", cfg, "mcp", *cfg.MCP)
	slog.New(slog.NewTextHandler(&textOut, nil)).Info("config", "config", cfg, "mcp", *cfg.MCP)
	renderings["slog json"], renderings["slog text"] = jsonOut.String(), textOut.String()
	for how, out := range renderings {
		if strings.Contains(out, "TOPSECRETMCPTOKEN") {
			t.Errorf("%s leaks the token: %s", how, out)
		}
	}
}
