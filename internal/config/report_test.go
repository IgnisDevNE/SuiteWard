package config

import (
	"strings"
	"testing"
)

func TestLoadReportBaseURLIsAbsentWhenUnset(t *testing.T) {
	cfg, warnings, err := fakeEnv{vars: required(t)}.load()
	if err != nil || len(warnings) != 0 || cfg.ReportBaseURL != "" {
		t.Fatalf("Load: base %q, warnings %v, error %v; want no base URL", cfg.ReportBaseURL, warnings, err)
	}
}

func TestLoadReportBaseURLAcceptsAnInstanceOnTheLoopback(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":  "http://127.0.0.1:8080",
		"http://localhost:18080": "http://localhost:18080",
		"http://127.0.0.1:8080/": "http://127.0.0.1:8080",
		"http://LocalHost:80":    "http://localhost:80",
		"http://127.0.0.1:65535": "http://127.0.0.1:65535",
	} {
		t.Run(in, func(t *testing.T) {
			vars := required(t)
			vars["SUITEWARD_REPORT_BASE_URL"] = in
			cfg, warnings, err := fakeEnv{vars: vars}.load()
			if err != nil || len(warnings) != 0 || cfg.ReportBaseURL != want {
				t.Fatalf("Load: base %q, warnings %v, error %v; want %q", cfg.ReportBaseURL, warnings, err, want)
			}
		})
	}
}

func TestLoadReportBaseURLRefusesAnythingButALoopbackHostAndPort(t *testing.T) {
	for _, in := range []string{
		"https://127.0.0.1:8080", "http://127.0.0.1", "http://127.0.0.1:", "http://example.com:8080", "http://127.0.0.1.example.com:8080",
		"http://0.0.0.0:8080", "http://[::1]:8080", "http://127.0.0.1:8080/reports", "http://user@127.0.0.1:8080", "http://127.0.0.1:8080?x=1",
		"http://127.0.0.1:8080#f", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:08080", "127.0.0.1:8080", "http://127.0.0.1:8080 ", "http:///",
	} {
		t.Run(in, func(t *testing.T) {
			vars := required(t)
			vars["SUITEWARD_REPORT_BASE_URL"] = in
			_, _, err := fakeEnv{vars: vars}.load()
			if err == nil || !strings.Contains(err.Error(), "SUITEWARD_REPORT_BASE_URL") {
				t.Fatalf("Load = %v, want an error that names SUITEWARD_REPORT_BASE_URL", err)
			}
		})
	}
}
