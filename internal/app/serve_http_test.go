package app

import (
	"net/http"
	"testing"
	"time"
)

// A connection that stalls while sending, reading or idling must not hold the listener forever. The two servers
// (probes and MCP) share one constructor, so one test covers both.
func TestHTTPServersBoundEveryPhaseOfAConnection(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(handler)

	if srv.Handler != handler {
		t.Fatal("the server does not serve the handler it was given")
	}
	for name, timeout := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if timeout <= 0 {
			t.Errorf("%s = %v; want a bound", name, timeout)
		}
	}
	if srv.ReadTimeout < srv.ReadHeaderTimeout {
		t.Errorf("ReadTimeout %v is shorter than ReadHeaderTimeout %v, which it includes", srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	// A probe or MCP call waits for the store at most storeTimeout; the write bound must leave room for that.
	if srv.WriteTimeout <= storeTimeout {
		t.Errorf("WriteTimeout %v does not exceed storeTimeout %v: it would cut a slow but legitimate answer", srv.WriteTimeout, storeTimeout)
	}
}
