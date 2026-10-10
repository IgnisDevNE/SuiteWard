package main

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
)

// logHeaders is the only set of response headers that may reach the log.
var logHeaders = []string{
	"ETag", "Date", "Last-Modified", "X-GitHub-Request-Id",
	"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Used",
	"X-RateLimit-Reset", "X-RateLimit-Resource", "Vary",
}

// logger writes one JSON object per line.
type logger struct {
	mu    sync.Mutex
	w     io.Writer
	start time.Time // process start; its monotonic reading anchors mono_ms
}

func (l *logger) emit(event string, fields map[string]any) {
	rec := map[string]any{"ts": time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), "event": event}
	maps.Copy(rec, fields)
	b, err := json.Marshal(rec)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"ts": rec["ts"], "event": "log_error", "error": err.Error()})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// http logs one GitHub call. Request headers and bodies never get here;
// of the response only the whitelisted headers do.
func (l *logger) http(began time.Time, method, path string, status int, etagSent bool, h http.Header, message string) {
	headers := map[string]string{}
	for _, name := range logHeaders {
		if v := h.Values(name); len(v) > 0 {
			headers[name] = strings.Join(v, ", ")
		}
	}
	f := map[string]any{
		"method": method, "path": path, "status": status, "etag_sent": etagSent, "headers": headers,
		"mono_ms": ms(began.Sub(l.start)), "dur_ms": ms(time.Since(began)),
	}
	if message != "" {
		f["message"] = message
	}
	l.emit("http", f)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
