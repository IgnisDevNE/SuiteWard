package river

import (
	"testing"
	"time"
)

func TestStuckJobsAreRescuedOneMinuteAfterTheJobTimeout(t *testing.T) {
	for name, test := range map[string]struct {
		config Config
		want   time.Duration
	}{
		"default":  {Config{JobTimeout: time.Minute}, 2 * time.Minute},
		"short":    {Config{JobTimeout: 5 * time.Second}, 65 * time.Second},
		"injected": {Config{JobTimeout: 5 * time.Second, RescueStuckJobsAfter: time.Second * 10}, 10 * time.Second},
	} {
		if got := test.config.rescueStuckJobsAfter(); got != test.want {
			t.Errorf("%s: rescue after %v, want %v", name, got, test.want)
		}
	}
}

// River's daily maintenance reindexes its indexes with REINDEX INDEX CONCURRENTLY, which needs ownership of them. The
// service connects as a role that holds no more than data access, so the reindexer must have nothing to reindex: a
// nil list would select River's defaults, an empty non-nil one selects none.
func TestTheReindexerHasNoIndexToReindex(t *testing.T) {
	c := clientConfig(Config{JobTimeout: time.Minute}, nil, nil).WithDefaults()
	if c.ReindexerIndexNames == nil || len(c.ReindexerIndexNames) != 0 {
		t.Fatalf("ReindexerIndexNames = %#v; want an empty, non-nil list", c.ReindexerIndexNames)
	}
}
