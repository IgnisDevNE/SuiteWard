package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
)

// get makes a request with a plain client and gives the error that net/http returns for it, which is what client.send wraps.
func get(ctx context.Context, t *testing.T, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("GET %s was answered; want a transport error", url)
	}
	return err
}

// A result is posted again when the service may yet take it: it answered 503, or the call got no answer. It is not when no wait can change
// the answer: a certificate that the client does not trust (a misconfigured URL or an interception), or any other answer.
func TestAResultIsPostedAgainOnlyWhenTheServiceMayYetTakeIt(t *testing.T) {
	untrusted := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	untrusted.Config.ErrorLog = log.New(io.Discard, "", 0) // the server logs each handshake that the client refuses
	untrusted.StartTLS()
	t.Cleanup(untrusted.Close)
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	gone.Close() // nothing listens at its address now
	live := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(live.Close)
	expired, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"a 503":                              {fmt.Errorf("result: %w (status 503)", client.ErrUnavailable), true},
		"a connection that was refused":      {get(t.Context(), t, gone.URL), true},
		"a call that timed out":              {get(expired, t, live.URL), true},
		"a certificate that is not trusted":  {get(t.Context(), t, untrusted.URL), false},
		"a lost lease":                       {fmt.Errorf("result: %w (status 409)", client.ErrLeaseLost), false},
		"a refusal of the request":           {fmt.Errorf("result: %w (status 400)", client.ErrInvalid), false},
		"a failure of the service":           {fmt.Errorf("result: %w (status 500)", client.ErrServer), false},
		"an answer that breaks the contract": {fmt.Errorf("result: %w", client.ErrBadAnswer), false},
		"no error":                           {nil, false},
		"another error":                      {errors.New("something else"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := notTaken(tc.err); got != tc.want {
				t.Errorf("notTaken(%v) = %t; want %t", tc.err, got, tc.want)
			}
		})
	}
}
